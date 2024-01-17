package catbird

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"log"

	"github.com/google/uuid"
	"nhooyr.io/websocket"
)

type Config struct {
	ErrorHandler  func(error)
	MessageBuffer int
	WriteTimeout  time.Duration
	Bridge        Bridge
}

type Hub struct {
	id            string
	errorHandler  func(error)
	messageBuffer int
	writeTimeout  time.Duration
	bridge        Bridge
	subscribersMu sync.RWMutex
	subscribers   map[*subscriber]struct{}
	topics        map[string]map[*subscriber]struct{}
	presenceMap   *presenceMap
}

type Bridge interface {
	Send(string, string, []byte) error
	Receive(string, func(string, []byte)) error
	SendHeartbeat(string, string, []string) error
	ReceiveHeartbeat(string, func(string, []string)) error
}

type subscriber struct {
	id        string
	msgs      chan []byte
	closeSlow func()
	userID    string
	topics    []string
}

func New(c Config) (*Hub, error) {
	if c.ErrorHandler == nil {
		c.ErrorHandler = func(err error) {
			log.Print(fmt.Errorf("catbird: %w", err))
		}
	}
	if c.MessageBuffer == 0 {
		c.MessageBuffer = 16
	}
	if c.WriteTimeout == 0 {
		c.WriteTimeout = time.Second * 5
	}

	h := &Hub{
		id:            uuid.NewString(),
		errorHandler:  c.ErrorHandler,
		messageBuffer: c.MessageBuffer,
		writeTimeout:  c.WriteTimeout,
		bridge:        c.Bridge,
		subscribers:   make(map[*subscriber]struct{}),
		topics:        make(map[string]map[*subscriber]struct{}),
		presenceMap:   newPresenceMap(),
	}

	if h.bridge != nil {
		if err := h.bridge.Receive(h.id, h.send); err != nil {
			return nil, err
		}
		if err := h.bridge.ReceiveHeartbeat(h.id, h.heartbeat); err != nil {
			return nil, err
		}
	}

	return h, nil
}

func (h *Hub) Stop() {
	h.presenceMap.Stop()
}

func (h *Hub) HandleWebsocket(w http.ResponseWriter, r *http.Request, userID string, topics []string) error {
	err := h.handleWebsocket(w, r, userID, topics)
	if errors.Is(err, context.Canceled) ||
		websocket.CloseStatus(err) == websocket.StatusNormalClosure ||
		websocket.CloseStatus(err) == websocket.StatusGoingAway {
		return nil
	}
	return err
}

func (h *Hub) handleWebsocket(w http.ResponseWriter, r *http.Request, userID string, topics []string) error {
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
					if err := h.bridge.SendHeartbeat(h.id, userID, topics); err != nil {
						h.errorHandler(err)
					}
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
	if h.bridge != nil {
		if err := h.bridge.Send(h.id, topic, msg); err != nil {
			h.errorHandler(err)
		}
	}

	h.send(topic, msg)
}

func (h *Hub) SendString(topic string, msg string) {
	h.Send(topic, []byte(msg))
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
