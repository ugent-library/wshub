package catbird

import (
	"sync"
	"time"
)

type presenceMap struct {
	ttl    time.Duration
	topics map[string]map[string]int64
	mu     sync.RWMutex
	stop   chan bool
}

func newPresenceMap() *presenceMap {
	m := &presenceMap{
		ttl:    1 * time.Second,
		topics: make(map[string]map[string]int64),
		stop:   make(chan bool),
	}
	m.start()
	return m
}

func (m *presenceMap) start() {
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				m.clean()
			case <-m.stop:
				return
			}
		}
	}()
}

func (m *presenceMap) Stop() {
	m.stop <- true
}

func (m *presenceMap) Add(topic, userID string) {
	m.mu.Lock()

	users, ok := m.topics[topic]
	if ok {
		users[userID] = time.Now().Add(m.ttl).UnixMilli()
	} else {
		m.topics[topic] = map[string]int64{userID: time.Now().Add(m.ttl).UnixMilli()}
	}

	m.mu.Unlock()
}

func (m *presenceMap) Get(topic string) (userIDs []string) {
	now := time.Now().UnixMilli()

	m.mu.RLock()

	if users, ok := m.topics[topic]; ok {
		for userID, expires := range users {
			if expires >= now {
				userIDs = append(userIDs, userID)
			}
		}
	}

	m.mu.RUnlock()

	return
}

func (m *presenceMap) clean() {
	now := time.Now().UnixMilli()

	m.mu.Lock()

	for topic, users := range m.topics {
		for userID, expires := range users {
			if now >= expires {
				delete(users, userID)
			}
		}
		if len(users) == 0 {
			delete(m.topics, topic)
		}
	}

	m.mu.Unlock()
}
