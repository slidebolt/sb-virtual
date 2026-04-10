package virtual

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	domain "github.com/slidebolt/sb-domain"
	logcfg "github.com/slidebolt/sb-logging"
	logging "github.com/slidebolt/sb-logging-sdk"
	logserver "github.com/slidebolt/sb-logging/server"
	messenger "github.com/slidebolt/sb-messenger-sdk"
	storage "github.com/slidebolt/sb-storage-sdk"
	storageserver "github.com/slidebolt/sb-storage-server"
)

type blob struct {
	key  string
	data json.RawMessage
}

func (b blob) Key() string                  { return b.key }
func (b blob) MarshalJSON() ([]byte, error) { return b.data, nil }

func saveEntity(t *testing.T, store storage.Storage, e domain.Entity) {
	t.Helper()
	data, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(blob{key: e.Key(), data: data}); err != nil {
		t.Fatal(err)
	}
	if len(e.Labels) > 0 || len(e.Meta) > 0 {
		profile := map[string]any{}
		if len(e.Labels) > 0 {
			profile["labels"] = e.Labels
		}
		if len(e.Meta) > 0 {
			profile["meta"] = e.Meta
		}
		pd, _ := json.Marshal(profile)
		if err := store.SetProfile(blob{key: e.Key()}, json.RawMessage(pd)); err != nil {
			t.Fatalf("setprofile %s: %v", e.Key(), err)
		}
	}
}

func TestBasementLikeFanoutLogsControlEntityRecipient(t *testing.T) {
	msg, err := messenger.Mock()
	if err != nil {
		t.Fatal(err)
	}
	defer msg.Close()

	store, err := storageserver.Mock(msg)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	logSvc, err := logserver.New(logcfg.Config{Target: "memory"})
	if err != nil {
		t.Fatalf("log server: %v", err)
	}
	logger := logSvc.Store()

	handler := NewHandlerWithLogger(msg, store, logger)
	sub, err := handler.Subscribe()
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Unsubscribe()

	saveEntity(t, store, domain.Entity{
		ID:       "switch_main_basement_3558733165",
		Plugin:   "plugin-esphome",
		DeviceID: "switch_main_basement",
		Type:     "binary_sensor",
		Name:     "Main Switch SingleClickActivated",
		Labels: map[string][]string{
			"PluginAutomation": {"Basement"},
		},
		State: domain.BinarySensor{On: false},
	})
	saveEntity(t, store, domain.Entity{
		ID:       "basement-light-1",
		Plugin:   "plugin-esphome",
		DeviceID: "basement-light-1",
		Type:     "light",
		Name:     "Basement Light 1",
		Labels: map[string][]string{
			"PluginAutomation": {"Basement"},
		},
		State: domain.Light{Power: false},
	})
	saveEntity(t, store, domain.Entity{
		ID:       "basement",
		Plugin:   "plugin-automation",
		DeviceID: "group",
		Type:     "light",
		Name:     "Basement",
		Target:   json.RawMessage(`{"pattern":"","where":[{"field":"labels.PluginAutomation","op":"eq","value":"Basement"}]}`),
		State:    domain.Light{Power: false},
	})

	headers := messenger.Headers{messenger.HeaderTraceID: "trace-basement-1"}
	if err := msg.PublishWithHeaders("plugin-automation.group.basement.command.light_turn_off", []byte(`{}`), headers); err != nil {
		t.Fatalf("publish: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		events, err := logger.List(context.Background(), logging.ListRequest{
			Kind:    "fanout.published",
			Entity:  "plugin-automation.group.basement",
			TraceID: "trace-basement-1",
		})
		if err != nil {
			t.Fatalf("list logs: %v", err)
		}
		if len(events) >= 2 {
			var sawControl bool
			var sawLight bool
			for _, event := range events {
				t.Logf("event: %s", formatEvent(event))
				if got := event.Data["recipient"]; got == "plugin-esphome.switch_main_basement.switch_main_basement_3558733165" {
					sawControl = true
				}
				if got := event.Data["recipient"]; got == "plugin-esphome.basement-light-1.basement-light-1" {
					sawLight = true
				}
			}
			if sawControl && sawLight {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}

	events, err := logger.List(context.Background(), logging.ListRequest{
		Kind:    "fanout.published",
		Entity:  "plugin-automation.group.basement",
		TraceID: "trace-basement-1",
	})
	if err != nil {
		t.Fatalf("list final logs: %v", err)
	}
	for _, event := range events {
		t.Logf("event: %s", formatEvent(event))
	}
	t.Fatalf("expected fanout logs to include both control entity and light recipient, got %+v", events)
}

func formatEvent(event logging.Event) string {
	data, err := json.Marshal(event)
	if err != nil {
		return fmt.Sprintf("marshal event %s: %v", event.ID, err)
	}
	return string(data)
}
