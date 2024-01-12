package catbird

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"nhooyr.io/websocket"
)

type Config struct {
	MessageBuffer int
	WriteTimeout  time.Duration
	// Secret should be a random 256 bit key.
	Secret []byte
	// Limiter       *rate.Limiter
	ErrorFunc func(error)
	Bridge    Bridge
}

type Hub struct {
	id            string
	messageBuffer int
	writeTimeout  time.Duration
	secret        []byte
	// limiter       *rate.Limiter
	errorFunc     func(error)
	bridge        Bridge
	subscribersMu sync.RWMutex
	subscribers   map[*subscriber]struct{}
	topics        map[string]map[*subscriber]struct{}
	presenceMap   *presenceMap
}

type Bridge interface {
	Send(string, string, []byte)
	Receive(string, func(string, []byte))
	SendHeartbeat(string, string, []string)
	ReceiveHeartbeat(string, func(string, []string))
}

type subscriber struct {
	id        string
	msgs      chan []byte
	closeSlow func()
	userID    string
	topics    []string
}

func New(c Config) *Hub {
	if c.MessageBuffer == 0 {
		c.MessageBuffer = 16
	}
	if c.WriteTimeout == 0 {
		c.WriteTimeout = time.Second * 5
	}
	// if c.Limiter == nil {
	// 	c.Limiter = rate.NewLimiter(rate.Every(time.Millisecond*100), 8)
	// }
	if c.ErrorFunc == nil {
		c.ErrorFunc = func(err error) {
			panic(err)
		}
	}

	h := &Hub{
		id:            uuid.NewString(),
		messageBuffer: c.MessageBuffer,
		writeTimeout:  c.WriteTimeout,
		secret:        c.Secret,
		// limiter:       c.Limiter,
		errorFunc:   c.ErrorFunc,
		bridge:      c.Bridge,
		subscribers: make(map[*subscriber]struct{}),
		topics:      make(map[string]map[*subscriber]struct{}),
		presenceMap: newPresenceMap(),
	}

	if h.bridge != nil {
		h.bridge.Receive(h.id, h.send)
		h.bridge.ReceiveHeartbeat(h.id, h.heartbeat)
	}

	return h
}

func (h *Hub) HandleWebsocket(w http.ResponseWriter, r *http.Request, token string) {
	err := h.handleWebsocket(w, r, token)
	if errors.Is(err, context.Canceled) ||
		websocket.CloseStatus(err) == websocket.StatusNormalClosure ||
		websocket.CloseStatus(err) == websocket.StatusGoingAway {
		return
	}
	if err != nil {
		h.errorFunc(err)
	}
}

func (h *Hub) handleWebsocket(w http.ResponseWriter, r *http.Request, token string) error {
	userID, topics, err := h.Decrypt(token)
	if err != nil {
		return err
	}

	var mu sync.Mutex
	var conn *websocket.Conn
	var closed bool

	ctx := r.Context()

	s := &subscriber{
		id:     uuid.NewString(),
		userID: userID,
		topics: topics,
		msgs:   make(chan []byte, h.messageBuffer),
		closeSlow: func() {
			mu.Lock()
			defer mu.Unlock()
			closed = true
			if conn != nil {
				conn.Close(websocket.StatusPolicyViolation, "connection too slow")
			}
		},
	}

	h.addSubscriber(s)
	defer h.deleteSubscriber(s)

	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		return err
	}
	mu.Lock()
	if closed {
		mu.Unlock()
		return net.ErrClosed
	}
	conn = c
	mu.Unlock()
	defer conn.CloseNow()

	ctx = conn.CloseRead(ctx)

	// heartbeat
	go func() {
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				if h.bridge != nil {
					h.bridge.SendHeartbeat(h.id, userID, topics)
				}
				h.heartbeat(userID, topics)
			case <-ctx.Done():
				return
			}
		}
	}()

	for {
		select {
		case msg := <-s.msgs:
			err := writeWithTimeout(ctx, conn, time.Second*5, msg)
			if err != nil {
				return err
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (h *Hub) heartbeat(userID string, topics []string) {
	for _, topic := range topics {
		h.presenceMap.Add(topic, userID)
	}
}

func (h *Hub) addSubscriber(s *subscriber) {
	h.subscribersMu.Lock()

	h.subscribers[s] = struct{}{}

	for _, topic := range s.topics {
		if subs, ok := h.topics[topic]; ok {
			subs[s] = struct{}{}
		} else {
			h.topics[topic] = map[*subscriber]struct{}{s: {}}
		}
	}

	h.subscribersMu.Unlock()
}

func (h *Hub) deleteSubscriber(s *subscriber) {
	h.subscribersMu.Lock()

	delete(h.subscribers, s)

	for _, topic := range s.topics {
		delete(h.topics[topic], s)
	}

	h.subscribersMu.Unlock()
}

func writeWithTimeout(ctx context.Context, c *websocket.Conn, timeout time.Duration, msg []byte) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	return c.Write(ctx, websocket.MessageText, msg)
}

func (h *Hub) Presence(topic string) []string {
	return h.presenceMap.Get(topic)
}

func (h *Hub) Send(topic string, msg []byte) {
	// h.limiter.Wait(context.Background())

	if h.bridge != nil {
		h.bridge.Send(h.id, topic, msg)
	}

	h.send(topic, msg)
}

func (h *Hub) send(topic string, msg []byte) {
	h.subscribersMu.RLock()

	if topic == "*" {
		for s := range h.subscribers {
			s.msgs <- msg
		}
	} else if subs, ok := h.topics[topic]; ok {
		for s := range subs {
			s.msgs <- msg
		}
	}

	h.subscribersMu.RUnlock()
}

func (h *Hub) Encrypt(userID string, topics []string) (string, error) {
	plaintext := []byte(userID + "|" + strings.Join(topics, "|"))

	// Create a new AES cipher block from the secret key.
	block, err := aes.NewCipher(h.secret)
	if err != nil {
		return "", err
	}

	// Wrap the cipher block in Galois Counter Mode.
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("catbird: %w", err)
	}

	// Create a unique nonce containing 12 random bytes.
	nonce := make([]byte, gcm.NonceSize())
	_, err = io.ReadFull(rand.Reader, nonce)
	if err != nil {
		return "", fmt.Errorf("catbird: %w", err)
	}

	// Encrypt plaintext using aesGCM.Seal(). By passing the nonce as the first
	// parameter, the ciphertext will be appended to the nonce so
	// that the encrypted message will be in the format
	// "{nonce}{encrypted message}".
	ciphertext := gcm.Seal(nonce, nonce, plaintext, nil)

	return base64.URLEncoding.EncodeToString(ciphertext), nil
}

func (h *Hub) Decrypt(token string) (string, []string, error) {
	ciphertext, err := base64.URLEncoding.DecodeString(token)
	if err != nil {
		return "", nil, fmt.Errorf("catbird: %w", err)
	}

	// Create a new AES cipher block from the secret key.
	block, err := aes.NewCipher(h.secret)
	if err != nil {
		return "", nil, fmt.Errorf("catbird: %w", err)
	}

	// Wrap the cipher block in Galois Counter Mode.
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", nil, fmt.Errorf("catbird: %w", err)
	}

	nonceSize := gcm.NonceSize()

	// Avoid potential index out of range panic in the next step.
	if len(ciphertext) < nonceSize {
		return "", nil, errors.New("catbird: invalid secret")
	}

	// Split ciphertext in nonce and encrypted data and use gcm.Open() to
	// decrypt and authenticate the data.
	plaintext, err := gcm.Open(nil, ciphertext[:nonceSize], ciphertext[nonceSize:], nil)
	if err != nil {
		return "", nil, fmt.Errorf("catbird: %w", err)
	}

	parts := strings.Split(string(plaintext), "|")
	return parts[0], parts[1:], nil
}
