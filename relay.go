package nostr

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"time"

	s "github.com/SaveTheRbtz/generic-sync-map-go"
	"github.com/gorilla/websocket"
)

type Status int

const (
	PublishStatusSent      Status = 0
	PublishStatusFailed    Status = -1
	PublishStatusSucceeded Status = 1
)

func (s Status) String() string {
	switch s {
	case PublishStatusSent:
		return "sent"
	case PublishStatusFailed:
		return "failed"
	case PublishStatusSucceeded:
		return "success"
	}

	return "unknown"
}

type Relay struct {
	URL string

	Connection    *Connection
	subscriptions s.MapOf[string, *Subscription]

	Notices chan string
}

func NewRelay(url string) *Relay {
	return &Relay{
		URL:           NormalizeURL(url),
		subscriptions: s.MapOf[string, *Subscription]{},
	}
}

func (r *Relay) Connect() error {
	if r.URL == "" {
		return fmt.Errorf("invalid relay URL '%s'", r.URL)
	}

	socket, _, err := websocket.DefaultDialer.Dial(r.URL, nil)
	if err != nil {
		return fmt.Errorf("error opening websocket to '%s': %w", r.URL, err)
	}

	conn := NewConnection(socket)

	for {
		typ, message, err := conn.socket.ReadMessage()
		if err != nil {
			return fmt.Errorf("read error: %w", err)
		}
		if typ == websocket.PingMessage {
			conn.WriteMessage(websocket.PongMessage, nil)
			continue
		}

		if typ != websocket.TextMessage || len(message) == 0 || message[0] != '[' {
			continue
		}

		var jsonMessage []json.RawMessage
		err = json.Unmarshal(message, &jsonMessage)
		if err != nil {
			continue
		}

		if len(jsonMessage) < 2 {
			continue
		}

		var label string
		json.Unmarshal(jsonMessage[0], &label)

		switch label {
		case "NOTICE":
			var content string
			json.Unmarshal(jsonMessage[1], &content)
			r.Notices <- content
		case "EVENT":
			if len(jsonMessage) < 3 {
				continue
			}

			var channel string
			json.Unmarshal(jsonMessage[1], &channel)
			if subscription, ok := r.subscriptions.Load(channel); ok {
				var event Event
				json.Unmarshal(jsonMessage[2], &event)

				// check signature of all received events, ignore invalid
				ok, err := event.CheckSignature()
				if !ok {
					errmsg := ""
					if err != nil {
						errmsg = err.Error()
					}
					log.Printf("bad signature: %s", errmsg)
					continue
				}

				// check if the event matches the desired filter, ignore otherwise
				if !subscription.filters.Match(&event) {
					continue
				}

				if !subscription.stopped {
					subscription.Events <- event
				}
			}
		}
	}
}

func (r Relay) Publish(event Event) chan Status {
	statusChan := make(chan Status)

	go func() {
		err := r.Connection.WriteJSON([]interface{}{"EVENT", event})
		if err != nil {
			statusChan <- PublishStatusFailed
			close(statusChan)
		}
		statusChan <- PublishStatusSent

		sub := r.Subscribe(Filters{Filter{IDs: []string{event.ID}}})
		for {
			select {
			case receivedEvent := <-sub.Events:
				if receivedEvent.ID == event.ID {
					statusChan <- PublishStatusSucceeded
					close(statusChan)
					break
				} else {
					continue
				}
			case <-time.After(5 * time.Second):
				close(statusChan)
				break
			}
			break
		}
	}()

	return statusChan
}

func (r *Relay) Subscribe(filters Filters) *Subscription {
	random := make([]byte, 7)
	rand.Read(random)
	id := hex.EncodeToString(random)
	return r.subscribe(id, filters)
}

func (r *Relay) subscribe(id string, filters Filters) *Subscription {
	sub := Subscription{}
	sub.id = id

	sub.Events = make(chan Event)
	r.subscriptions.Store(sub.id, &sub)

	sub.Sub(filters)
	return &sub
}

func (r *Relay) Close() error {
	return r.Connection.Close()
}
