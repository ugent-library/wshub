package natsbridge

import (
	"encoding/json"
	"strings"

	"github.com/nats-io/nats.go"
)

type Bridge struct {
	conn *nats.Conn
}

type heartbeat struct {
	HubID  string   `json:"hub_id"`
	UserID string   `json:"user_id"`
	Topics []string `json:"topics"`
}

// TODO drain, ctx
func New(url string) (*Bridge, error) {
	if url == "" {
		url = nats.DefaultURL
	}
	conn, err := nats.Connect(url)
	if err != nil {
		return nil, err
	}
	return &Bridge{
		conn: conn,
	}, nil
}

// TODO error
func (b *Bridge) Send(hubID, topic string, msg []byte) {
	hdr := make(nats.Header)
	hdr.Set("Hub-Id", hubID)
	b.conn.PublishMsg(&nats.Msg{
		Subject: "hub.topic." + topic,
		Header:  hdr,
		Data:    msg,
	})
}

// TODO error
func (b *Bridge) Receive(hubID string, fn func(string, []byte)) {
	b.conn.Subscribe("hub.topic.>", func(msg *nats.Msg) {
		if msg.Header.Get("Hub-Id") != hubID {
			topic := strings.TrimPrefix(msg.Subject, "hub.topic.")
			fn(topic, msg.Data)
		}

	})
}

// TODO error
func (b *Bridge) SendHeartbeat(hubID, userID string, topics []string) {
	hdr := make(nats.Header)
	hdr.Set("Hub-Id", hubID)
	data, _ := json.Marshal(&heartbeat{
		HubID:  hubID,
		UserID: userID,
		Topics: topics,
	})
	b.conn.PublishMsg(&nats.Msg{
		Subject: "hub.heartbeat",
		Header:  hdr,
		Data:    data,
	})
}

// TODO error
func (b *Bridge) ReceiveHeartbeat(hubID string, fn func(string, string, []string)) {
	b.conn.Subscribe("hub.heartbeat", func(msg *nats.Msg) {
		if msg.Header.Get("Hub-Id") != hubID {
			var hb heartbeat
			json.Unmarshal(msg.Data, &hb)
			fn(hb.HubID, hb.UserID, hb.Topics)
		}
	})
}
