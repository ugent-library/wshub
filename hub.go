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

type Hub struct {
	id            string
	messageBuffer int
	writeTimeout  time.Duration
	limiter       *rate.Limiter
	errorFunc     func(error)
	userFunc      func(*http.Request) (string, []string)
	subscribersMu sync.RWMutex
	subscribers   map[*subscriber]struct{}
	topics        map[string]subscriptions
}

type subscriptions struct {
	subscribers  map[string]Subscription
	users        map[string]int
	onNewUser    func(string, Subscription)
	onRemoveUser func(string, Subscription)
}

type Subscription interface {
	ID() string
	UserID() string
	Send([]byte)
}

type subscriber struct {
	id        string
	msgs      chan []byte
	closeSlow func()
	userID    string
	topics    []string
}

func (s *subscriber) ID() string {
	return s.id
}

func (s *subscriber) UserID() string {
	return s.userID
}

func (s *subscriber) Send(msg []byte) {
	select {
	case s.msgs <- msg:
	default:
		go s.closeSlow()
	}
}

func NewHub() *Hub {
	return &Hub{
		id:            uuid.NewString(),
		messageBuffer: 16,
		writeTimeout:  time.Second * 5,
		limiter:       rate.NewLimiter(rate.Every(time.Millisecond*100), 8),
		errorFunc: func(err error) {
			panic(err)
		},
		userFunc: func(r *http.Request) (string, []string) {
			return "", nil
		},
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
		h.errorFunc(err)
	}
}

func (h *Hub) handleWebsocket(w http.ResponseWriter, r *http.Request) error {
	userID, topics := h.userFunc(r)

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

func (h *Hub) addSubscription(topic string, s Subscription) func() {
	if subs, ok := h.topics[topic]; ok {
		subs.subscribers[s.ID()] = s
		n := subs.users[s.UserID()]
		subs.users[s.UserID()] = n + 1

		if subs.onNewUser != nil && n == 0 {
			return func() {
				h.subscribersMu.RLock()
				defer h.subscribersMu.RUnlock()
				for _, sub := range subs.subscribers {
					subs.onNewUser(s.UserID(), sub)
				}
			}
		}
		return nil
	}

	subs := subscriptions{
		subscribers: map[string]Subscription{s.ID(): s},
		users:       map[string]int{s.UserID(): 1},
	}
	h.topics[topic] = subs

	return func() {
		h.subscribersMu.RLock()
		subs.onNewUser(s.UserID(), s)
		h.subscribersMu.RUnlock()

	}
}

func (h *Hub) addSubscriber(s *subscriber) {
	var thunks []func()

	h.subscribersMu.Lock()
	h.subscribers[s] = struct{}{}

	for _, topic := range s.topics {
		if thunk := h.addSubscription(topic, s); thunk != nil {
			thunks = append(thunks, thunk)
		}
	}

	h.subscribersMu.Unlock()

	for _, thunk := range thunks {
		thunk()
	}
}

func (h *Hub) deleteSubscriber(s *subscriber) {
	var thunks []func()

	h.subscribersMu.Lock()

	delete(h.subscribers, s)

	for _, topic := range s.topics {
		if subs, ok := h.topics[topic]; ok {
			delete(subs.subscribers, s.id)
			n := subs.users[s.userID] - 1
			if n == 0 {
				delete(subs.users, s.userID)
			} else {
				subs.users[s.userID] = n
			}

			if len(subs.subscribers) == 0 {
				delete(h.topics, topic)
			}

			if subs.onRemoveUser != nil && n == 0 {
				thunks = append(thunks, func() {
					h.subscribersMu.RLock()
					defer h.subscribersMu.RUnlock()
					for _, sub := range subs.subscribers {
						subs.onRemoveUser(s.UserID(), sub)
					}
				})
			}
		}
	}

	h.subscribersMu.Unlock()

	for _, thunk := range thunks {
		thunk()
	}
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
		s.Send(msg)
	}
}

func (h *Hub) SendTo(topic string, msg []byte) {
	h.subscribersMu.RLock()
	defer h.subscribersMu.RUnlock()

	if sub, ok := h.topics[topic]; ok {
		for _, s := range sub.subscribers {
			s.Send(msg)
		}
	}
}
