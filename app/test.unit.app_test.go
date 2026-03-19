package app

import (
	"encoding/json"
	"sort"
	"sync"
	"testing"
	"time"

	domain "github.com/slidebolt/sb-domain"
	messenger "github.com/slidebolt/sb-messenger-sdk"
	storage "github.com/slidebolt/sb-storage-sdk"
	server "github.com/slidebolt/sb-storage-server"
)

func TestHelloManifest(t *testing.T) {
	h := New().Hello()
	if h.ID != "virtual" {
		t.Fatalf("id: got %q want %q", h.ID, "virtual")
	}
	if len(h.DependsOn) != 2 || h.DependsOn[0] != "messenger" || h.DependsOn[1] != "storage" {
		t.Fatalf("dependsOn: got %v want [messenger storage]", h.DependsOn)
	}
}

// testEnv wires up an in-process NATS + storage and a virtual handler.
type testEnv struct {
	msg   messenger.Messenger
	store storage.Storage
	app   *App
}

func setup(t *testing.T) *testEnv {
	t.Helper()
	msg, payload, err := messenger.MockWithPayload()
	if err != nil {
		t.Fatal(err)
	}

	store, err := server.Mock(msg)
	if err != nil {
		msg.Close()
		t.Fatal(err)
	}

	a := New()
	if _, err := a.OnStart(map[string]json.RawMessage{"messenger": payload}); err != nil {
		msg.Close()
		t.Fatal(err)
	}

	t.Cleanup(func() { a.OnShutdown() })
	return &testEnv{msg: msg, store: store, app: a}
}

type blob struct {
	key  string
	data json.RawMessage
}

func (b blob) Key() string                  { return b.key }
func (b blob) MarshalJSON() ([]byte, error) { return b.data, nil }

func collectMessages(t *testing.T, msg messenger.Messenger, pattern string) *collected {
	t.Helper()
	c := &collected{}
	sub, err := msg.Subscribe(pattern, func(m *messenger.Message) {
		c.mu.Lock()
		c.msgs = append(c.msgs, capturedMsg{Subject: m.Subject, Data: append([]byte{}, m.Data...)})
		c.mu.Unlock()
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sub.Unsubscribe() })
	return c
}

type capturedMsg struct {
	Subject string
	Data    []byte
}

type collected struct {
	mu   sync.Mutex
	msgs []capturedMsg
}

func (c *collected) wait(n int, timeout time.Duration) []capturedMsg {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		c.mu.Lock()
		if len(c.msgs) >= n {
			out := make([]capturedMsg, len(c.msgs))
			copy(out, c.msgs)
			c.mu.Unlock()
			return out
		}
		c.mu.Unlock()
		time.Sleep(5 * time.Millisecond)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.msgs
}

func saveEntity(t *testing.T, store storage.Storage, e domain.Entity) {
	t.Helper()
	data, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(blob{key: e.Key(), data: data}); err != nil {
		t.Fatal(err)
	}
}

func TestFanoutTargetQuery(t *testing.T) {
	env := setup(t)

	saveEntity(t, env.store, domain.Entity{
		ID: "bulb1", Plugin: "wiz", DeviceID: "room", Type: "light", Name: "Bulb 1",
		State: domain.Light{Power: true, Brightness: 200},
	})
	saveEntity(t, env.store, domain.Entity{
		ID: "bulb2", Plugin: "wiz", DeviceID: "room", Type: "light", Name: "Bulb 2",
		State: domain.Light{Power: true, Brightness: 200},
	})
	saveEntity(t, env.store, domain.Entity{
		ID: "all-lights", Plugin: "virtual", DeviceID: "room", Type: "light", Name: "All Lights",
		Target: json.RawMessage(`{"pattern":"wiz.room.*","where":[{"field":"type","op":"eq","value":"light"}]}`),
		State:  domain.Light{},
	})

	cap := collectMessages(t, env.msg, "wiz.room.*.command.>")
	env.msg.Publish("virtual.room.all-lights.command.light_set_brightness", []byte(`{"brightness":100}`))

	msgs := cap.wait(2, 2*time.Second)
	if len(msgs) < 2 {
		t.Fatalf("expected 2 fan-out commands, got %d", len(msgs))
	}
	sort.Slice(msgs, func(i, j int) bool { return msgs[i].Subject < msgs[j].Subject })
	if msgs[0].Subject != "wiz.room.bulb1.command.light_set_brightness" {
		t.Errorf("msg[0] subject = %q", msgs[0].Subject)
	}
	if msgs[1].Subject != "wiz.room.bulb2.command.light_set_brightness" {
		t.Errorf("msg[1] subject = %q", msgs[1].Subject)
	}
}
