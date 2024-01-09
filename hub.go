package hub

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
	"golang.org/x/time/rate"
	"nhooyr.io/websocket"
)

type Config struct {
	MessageBuffer int
	WriteTimeout  time.Duration
	Limiter       *rate.Limiter
	ErrorFunc     func(error)
	UserIDFunc    func(*http.Request) (string, []string)
	Bridge        Bridge
}

type Hub struct {
	id            string
	messageBuffer int
	writeTimeout  time.Duration
	limiter       *rate.Limiter
	errorFunc     func(error)
	userIDFunc    func(*http.Request) (string, []string)
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

func NewHub(c Config) *Hub {
	if c.MessageBuffer == 0 {
		c.MessageBuffer = 16
	}
	if c.WriteTimeout == 0 {
		c.WriteTimeout = time.Second * 5
	}
	if c.Limiter == nil {
		c.Limiter = rate.NewLimiter(rate.Every(time.Millisecond*100), 8)
	}
	if c.ErrorFunc == nil {
		c.ErrorFunc = func(err error) {
			panic(err)
		}
	}
	if c.UserIDFunc == nil {
		c.UserIDFunc = func(r *http.Request) (string, []string) {
			return "", nil
		}
	}

	h := &Hub{
		id:            uuid.NewString(),
		messageBuffer: c.MessageBuffer,
		writeTimeout:  c.WriteTimeout,
		limiter:       c.Limiter,
		errorFunc:     c.ErrorFunc,
		userIDFunc:    c.UserIDFunc,
		bridge:        c.Bridge,
		presenceMap:   newPresenceMap(),
	}

	if h.bridge != nil {
		h.bridge.Receive(h.id, h.Send)
		h.bridge.ReceiveHeartbeat(h.id, h.heartbeat)
	}

	return h
}

func (h *Hub) HandleWebsocket(w http.ResponseWriter, r *http.Request) {
	err := h.handleWebsocket(w, r)
	if errors.Is(err, context.Canceled) ||
		websocket.CloseStatus(err) == websocket.StatusNormalClosure ||
		websocket.CloseStatus(err) == websocket.StatusGoingAway {
		return
	}
	if err != nil {
		h.errorFunc(err)
	}
}

func (h *Hub) handleWebsocket(w http.ResponseWriter, r *http.Request) error {
	userID, topics := h.userIDFunc(r)

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

func (h *Hub) Send(topic string, msg []byte) {
	// h.limiter.Wait(context.Background())

	if h.bridge != nil {
		h.bridge.Send(h.id, topic, msg)
	}

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
