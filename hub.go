package hub

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
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
	UserFunc      func(*http.Request) (string, []string)
	OnAddUser     map[string]func(string, Subscription)
	OnRemoveUser  map[string]func(string, Subscription)
}

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
	onAddUser     map[string]func(string, Subscription)
	onRemoveUser  map[string]func(string, Subscription)
}

type subscriptions struct {
	subscribers  map[string]Subscription
	users        map[string]int
	onAddUser    func(string, Subscription)
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
	if c.UserFunc == nil {
		c.UserFunc = func(r *http.Request) (string, []string) {
			return "", nil
		}
	}

	return &Hub{
		id:            uuid.NewString(),
		messageBuffer: c.MessageBuffer,
		writeTimeout:  c.WriteTimeout,
		limiter:       c.Limiter,
		errorFunc:     c.ErrorFunc,
		userFunc:      c.UserFunc,
		onAddUser:     c.OnAddUser,
		onRemoveUser:  c.OnRemoveUser,
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

		if subs.onAddUser != nil && n == 0 {
			return func() {
				h.subscribersMu.RLock()
				defer h.subscribersMu.RUnlock()
				for _, sub := range subs.subscribers {
					subs.onAddUser(s.UserID(), sub)
				}
			}
		}
		return nil
	}

	subs := subscriptions{
		subscribers: map[string]Subscription{s.ID(): s},
		users:       map[string]int{s.UserID(): 1},
	}

	// find handlers for topic by matching longest prefix
	// TODO make more efficient
	var longestAddUserPrefix string
	for prefix := range h.onAddUser {
		if strings.HasPrefix(topic, prefix) && len(prefix) > len(longestAddUserPrefix) {
			longestAddUserPrefix = prefix
		}
	}
	subs.onAddUser = h.onAddUser[longestAddUserPrefix]

	var longestRemoveUserPrefix string
	for prefix := range h.onRemoveUser {
		if strings.HasPrefix(topic, prefix) && len(prefix) > len(longestRemoveUserPrefix) {
			longestRemoveUserPrefix = prefix
		}
	}
	subs.onRemoveUser = h.onRemoveUser[longestRemoveUserPrefix]

	h.topics[topic] = subs

	return func() {
		h.subscribersMu.RLock()
		subs.onAddUser(s.UserID(), s)
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

func (h *Hub) Broadcast(msg []byte) {
	h.limiter.Wait(context.Background())

	h.subscribersMu.RLock()
	defer h.subscribersMu.RUnlock()

	for s := range h.subscribers {
		s.Send(msg)
	}
}

func (h *Hub) Send(topic string, msg []byte) {
	h.subscribersMu.RLock()
	defer h.subscribersMu.RUnlock()

	if sub, ok := h.topics[topic]; ok {
		for _, s := range sub.subscribers {
			s.Send(msg)
		}
	}
}
