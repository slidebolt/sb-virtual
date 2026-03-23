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
	// Persist labels/meta in sidecar so they survive Save() stripping.
	profile := make(map[string]any)
	if len(e.Labels) > 0 {
		profile["labels"] = e.Labels
	}
	if len(e.Meta) > 0 {
		profile["meta"] = e.Meta
	}
	if len(profile) > 0 {
		pd, _ := json.Marshal(profile)
		if err := store.SetProfile(blob{key: e.Key()}, json.RawMessage(pd)); err != nil {
			t.Fatalf("setprofile %s: %v", e.Key(), err)
		}
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

func TestFanoutLightstripSetSegments(t *testing.T) {
	env := setup(t)

	// Save the 3 individual strip member lights
	for i, id := range []string{"lb-01", "lb-02", "lb-03"} {
		saveEntity(t, env.store, domain.Entity{
			ID: id, Plugin: "zb", DeviceID: "dev", Type: "light", Name: id,
			Labels: map[string][]string{"PluginAutomation": {"MainLB"}},
			State:  domain.Light{},
			Meta: map[string]json.RawMessage{
				"PluginAutomation:MainLB": json.RawMessage(`{"position":` + string(rune('0'+i+1)) + `,"entity":"light_strip"}`),
			},
		})
	}

	// Save the lightstrip virtual entity — has both Target query AND ordered Targets in state
	saveEntity(t, env.store, domain.Entity{
		ID: "mainlb", Plugin: "automation", DeviceID: "group", Type: "light_strip", Name: "MainLB",
		Target: json.RawMessage(`{"where":[{"field":"labels.PluginAutomation","op":"eq","value":"MainLB"}]}`),
		State: domain.LightStrip{
			Targets: []string{"zb.dev.lb-01", "zb.dev.lb-02", "zb.dev.lb-03"},
		},
	})

	cap := collectMessages(t, env.msg, "zb.dev.*.command.>")
	if err := env.msg.Flush(); err != nil {
		t.Fatal(err)
	}

	payload, _ := json.Marshal(domain.LightstripSetSegments{
		Power: true,
		Segments: []domain.Segment{
			{ID: 1, RGB: []int{255, 0, 0}, Brightness: 50},
			{ID: 2, RGB: []int{0, 255, 0}, Brightness: 80},
			{ID: 3, RGB: []int{0, 0, 255}, Brightness: 120},
		},
	})
	env.msg.Publish("automation.group.mainlb.command.lightstrip_set_segments", payload)

	// Expect 6 commands: light_set_rgb + light_set_brightness for each of the 3 segments
	msgs := cap.wait(6, 2*time.Second)
	sort.Slice(msgs, func(i, j int) bool { return msgs[i].Subject < msgs[j].Subject })

	if len(msgs) != 6 {
		t.Fatalf("expected 6 fan-out commands (rgb+brightness per segment), got %d: %v",
			len(msgs), func() []string {
				s := make([]string, len(msgs))
				for i, m := range msgs {
					s[i] = m.Subject
				}
				return s
			}())
	}

	// lb-01 should get red rgb + brightness 50
	assertSubject(t, msgs[0], "zb.dev.lb-01.command.light_set_brightness")
	assertSubject(t, msgs[1], "zb.dev.lb-01.command.light_set_rgb")
	var rgb domain.LightSetRGB
	json.Unmarshal(msgs[1].Data, &rgb)
	if rgb.R != 255 || rgb.G != 0 || rgb.B != 0 {
		t.Errorf("lb-01 rgb: got {%d,%d,%d} want {255,0,0}", rgb.R, rgb.G, rgb.B)
	}

	// lb-02 should get green
	assertSubject(t, msgs[2], "zb.dev.lb-02.command.light_set_brightness")
	assertSubject(t, msgs[3], "zb.dev.lb-02.command.light_set_rgb")
	json.Unmarshal(msgs[3].Data, &rgb)
	if rgb.R != 0 || rgb.G != 255 || rgb.B != 0 {
		t.Errorf("lb-02 rgb: got {%d,%d,%d} want {0,255,0}", rgb.R, rgb.G, rgb.B)
	}

	// lb-03 should get blue
	assertSubject(t, msgs[4], "zb.dev.lb-03.command.light_set_brightness")
	assertSubject(t, msgs[5], "zb.dev.lb-03.command.light_set_rgb")
	json.Unmarshal(msgs[5].Data, &rgb)
	if rgb.R != 0 || rgb.G != 0 || rgb.B != 255 {
		t.Errorf("lb-03 rgb: got {%d,%d,%d} want {0,0,255}", rgb.R, rgb.G, rgb.B)
	}
}

func assertSubject(t *testing.T, msg capturedMsg, want string) {
	t.Helper()
	if msg.Subject != want {
		t.Errorf("subject: got %q want %q", msg.Subject, want)
	}
}
