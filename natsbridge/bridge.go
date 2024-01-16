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

func (b *Bridge) Send(hubID, topic string, msg []byte) error {
	hdr := make(nats.Header)
	hdr.Set("Hub-Id", hubID)
	return b.conn.PublishMsg(&nats.Msg{
		Subject: "catbird.topic." + topic,
		Header:  hdr,
		Data:    msg,
	})
}

func (b *Bridge) Receive(hubID string, fn func(string, []byte)) error {
	_, err := b.conn.Subscribe("catbird.topic.>", func(msg *nats.Msg) {
		if msg.Header.Get("Hub-Id") != hubID {
			topic := strings.TrimPrefix(msg.Subject, "catbird.topic.")
			fn(topic, msg.Data)
		}
	})
	return err
}

func (b *Bridge) SendHeartbeat(hubID, userID string, topics []string) error {
	hdr := make(nats.Header)
	hdr.Set("Hub-Id", hubID)
	data, _ := json.Marshal(&heartbeat{
		HubID:  hubID,
		UserID: userID,
		Topics: topics,
	})
	return b.conn.PublishMsg(&nats.Msg{
		Subject: "catbird.heartbeat",
		Header:  hdr,
		Data:    data,
	})
}

func (b *Bridge) ReceiveHeartbeat(hubID string, fn func(string, []string)) error {
	_, err := b.conn.Subscribe("catbird.heartbeat", func(msg *nats.Msg) {
		if msg.Header.Get("Hub-Id") != hubID {
			var hb heartbeat
			json.Unmarshal(msg.Data, &hb)
			fn(hb.UserID, hb.Topics)
		}
	})
	return err
}
