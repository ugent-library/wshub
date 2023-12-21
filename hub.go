package hub

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sync"
	"time"

	"golang.org/x/time/rate"
	"nhooyr.io/websocket"
)

type Hub struct {
	messageBuffer  int
	writeTimeout   time.Duration
	publishLimiter *rate.Limiter
	errorHandler   func(error)
	subscribersMu  sync.Mutex
	subscribers    map[*subscriber]struct{}
}

type subscriber struct {
	msgs      chan []byte
	closeSlow func()
}

func NewHub() *Hub {
	return &Hub{
		messageBuffer:  16,
		writeTimeout:   time.Second * 5,
		publishLimiter: rate.NewLimiter(rate.Every(time.Millisecond*100), 8),
	}
}

func (h *Hub) HandleWebsocket(w http.ResponseWriter, r *http.Request) {
	err := h.handleWebsocket(w, r)
	if errors.Is(err, context.Canceled) ||
		websocket.CloseStatus(err) == websocket.StatusNormalClosure ||
		websocket.CloseStatus(err) == websocket.StatusGoingAway {
		return
	}
	if err != nil {
		h.errorHandler(err)
	}
}

func (h *Hub) handleWebsocket(w http.ResponseWriter, r *http.Request) error {
	var mu sync.Mutex
	var conn *websocket.Conn
	var closed bool

	ctx := r.Context()

	s := &subscriber{
		msgs: make(chan []byte, h.messageBuffer),
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
	h.subscribersMu.Unlock()
}

// deleteSubscriber deletes the given subscriber.
func (h *Hub) deleteSubscriber(s *subscriber) {
	h.subscribersMu.Lock()
	delete(h.subscribers, s)
	h.subscribersMu.Unlock()
}

func writeWithTimeout(ctx context.Context, c *websocket.Conn, timeout time.Duration, msg []byte) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	return c.Write(ctx, websocket.MessageText, msg)
}

func (h *Hub) Publish(msg []byte) {
	h.publishLimiter.Wait(context.Background())

	h.subscribersMu.Lock()
	defer h.subscribersMu.Unlock()

	for s := range h.subscribers {
		select {
		case s.msgs <- msg:
		default:
			go s.closeSlow()
		}
	}
}
