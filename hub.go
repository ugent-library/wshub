package hub

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"sync"
	"time"

	"golang.org/x/time/rate"
	"nhooyr.io/websocket"
)

type Hub struct {
	messageBuffer int
	writeTimeout  time.Duration
	limiter       *rate.Limiter
	errorFunc     func(error)
	topicFunc     func(*http.Request) []string
	subscribersMu sync.RWMutex
	subscribers   map[*subscriber]struct{}
	topics        map[string]subscriptions
}

type subscriber struct {
	msgs      chan []byte
	closeSlow func()
	topics    []string
}

type subscriptions struct {
	subscribers map[*subscriber]struct{}
}

func NewHub() *Hub {
	return &Hub{
		messageBuffer: 16,
		writeTimeout:  time.Second * 5,
		limiter:       rate.NewLimiter(rate.Every(time.Millisecond*100), 8),
		errorFunc: func(err error) {
			log.Panic(err)
		},
		topicFunc: func(r *http.Request) []string {
			return nil
		},
	}
}

func (h *Hub) HandleWebsocket(w http.ResponseWriter, r *http.Request) {
	err := h.handleWebsocket(w, r, h.topicFunc(r))
	if errors.Is(err, context.Canceled) ||
		websocket.CloseStatus(err) == websocket.StatusNormalClosure ||
		websocket.CloseStatus(err) == websocket.StatusGoingAway {
		return
	}
	if err != nil {
		h.errorFunc(err)
	}
}

func (h *Hub) handleWebsocket(w http.ResponseWriter, r *http.Request, topics []string) error {
	var mu sync.Mutex
	var conn *websocket.Conn
	var closed bool

	ctx := r.Context()

	s := &subscriber{
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

func (h *Hub) addSubscriber(s *subscriber) {
	h.subscribersMu.Lock()
	h.subscribers[s] = struct{}{}
	for _, t := range s.topics {
		if subs, ok := h.topics[t]; ok {
			subs.subscribers[s] = struct{}{}
		} else {
			h.topics[t] = subscriptions{subscribers: map[*subscriber]struct{}{s: {}}}
		}
	}
	h.subscribersMu.Unlock()
}

func (h *Hub) deleteSubscriber(s *subscriber) {
	h.subscribersMu.Lock()
	delete(h.subscribers, s)
	for _, t := range s.topics {
		if subs, ok := h.topics[t]; ok {
			delete(subs.subscribers, s)
		}
	}
	h.subscribersMu.Unlock()
}

func writeWithTimeout(ctx context.Context, c *websocket.Conn, timeout time.Duration, msg []byte) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	return c.Write(ctx, websocket.MessageText, msg)
}

func (h *Hub) Send(msg []byte) {
	h.limiter.Wait(context.Background())

	h.subscribersMu.RLock()
	defer h.subscribersMu.RUnlock()

	for s := range h.subscribers {
		select {
		case s.msgs <- msg:
		default:
			go s.closeSlow()
		}
	}
}

func (h *Hub) SendTo(topic string, msg []byte) {
	h.subscribersMu.RLock()
	defer h.subscribersMu.RUnlock()

	if sub, ok := h.topics[topic]; ok {
		for s := range sub.subscribers {
			select {
			case s.msgs <- msg:
			default:
				go s.closeSlow()
			}
		}
	}
}
