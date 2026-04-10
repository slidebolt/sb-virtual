// Package virtual provides command fanout for virtual entities.
// A virtual entity has either a Target query (group) or a LightStrip
// with ordered Targets (strip). When a command arrives for a virtual
// entity, the handler resolves the targets and publishes per-member
// commands on the same NATS bus.
package virtual

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync/atomic"
	"time"

	domain "github.com/slidebolt/sb-domain"
	logging "github.com/slidebolt/sb-logging-sdk"
	messenger "github.com/slidebolt/sb-messenger-sdk"
	storage "github.com/slidebolt/sb-storage-sdk"
)

// Handler intercepts commands to virtual entities and fans them out
// to the real member entities.
type Handler struct {
	msg   messenger.Messenger
	store storage.Storage
	log   logging.Store
}

var logSequence uint64

// NewHandler creates a Handler wired to the given messenger and storage.
func NewHandler(msg messenger.Messenger, store storage.Storage) *Handler {
	return &Handler{msg: msg, store: store}
}

func NewHandlerWithLogger(msg messenger.Messenger, store storage.Storage, logger logging.Store) *Handler {
	return &Handler{msg: msg, store: store, log: logger}
}

// Subscribe registers the wildcard subscription that drives fanout.
// Call this once after NewHandler. The returned Subscription can be
// used to unsubscribe when shutting down.
func (h *Handler) Subscribe() (messenger.Subscription, error) {
	return h.msg.Subscribe("*.*.*.command.>", h.HandleCommand)
}

// HandleCommand processes a single command message. It is safe to use
// as a messenger callback directly.
func (h *Handler) HandleCommand(m *messenger.Message) {
	idx := strings.LastIndex(m.Subject, ".command.")
	if idx < 0 {
		return
	}
	entityKey := m.Subject[:idx]
	action := m.Subject[idx+len(".command."):]
	traceID := messenger.TraceID(m.Headers)
	h.appendLog("command.received", "info", "virtual command received", entityKey, action, traceID, nil)

	data, err := h.store.Get(keyStr(entityKey))
	if err != nil {
		return
	}

	var entity domain.Entity
	if err := json.Unmarshal(data, &entity); err != nil {
		return
	}

	if len(entity.Target) > 0 {
		// Light strips carry both an ordered Targets slice (in state) and a Target
		// query. Ordered-segment dispatch must take priority over the query fanout.
		strip, ok := entity.State.(domain.LightStrip)
		if ok && len(strip.Targets) > 0 {
			h.fanoutStrip(strip, action, traceID, m.Data, m.Headers)
			return
		}
		h.fanoutQuery(entityKey, entity.Target, action, traceID, m.Data, m.Headers)
		return
	}

	strip, ok := entity.State.(domain.LightStrip)
	if !ok || len(strip.Targets) == 0 {
		return
	}
	h.fanoutStrip(strip, action, traceID, m.Data, m.Headers)
}

func (h *Handler) fanoutQuery(entityKey string, target json.RawMessage, action, traceID string, data []byte, headers messenger.Headers) {
	visited := make(map[string]bool)
	var finalTargets []string

	var resolve func(t json.RawMessage)
	resolve = func(t json.RawMessage) {
		var q storage.Query
		if err := json.Unmarshal(t, &q); err != nil {
			log.Printf("virtual: unmarshal target query: %v", err)
			return
		}
		members, err := h.store.Query(q)
		if err != nil {
			log.Printf("virtual: query members: %v", err)
			return
		}
		for _, member := range members {
			if visited[member.Key] {
				continue
			}
			visited[member.Key] = true
			var ent domain.Entity
			if err := json.Unmarshal(member.Data, &ent); err == nil && len(ent.Target) > 0 {
				resolve(ent.Target)
			} else {
				finalTargets = append(finalTargets, member.Key)
			}
		}
	}

	resolve(target)

	for _, targetKey := range finalTargets {
		h.appendLog("fanout.published", "info", "virtual fanout published", entityKey, action, traceID, map[string]any{
			"recipient": targetKey,
		})
		outHeaders := messenger.WithOrigin(headers, "sb-virtual", entityKey, action)
		h.msg.PublishWithHeaders(targetKey+".command."+action, data, outHeaders)
	}
}

func (h *Handler) fanoutStrip(strip domain.LightStrip, action, traceID string, data []byte, headers messenger.Headers) {
	if action == "lightstrip_set_segments" {
		h.fanoutSegments(strip, traceID, data, headers)
		return
	}
	for _, target := range strip.Targets {
		h.appendLog("fanout.published", "info", "virtual strip fanout published", "", action, traceID, map[string]any{
			"recipient": target,
		})
		outHeaders := messenger.WithOrigin(headers, "sb-virtual", target, action)
		h.msg.PublishWithHeaders(target+".command."+action, data, outHeaders)
	}
}

func (h *Handler) fanoutSegments(strip domain.LightStrip, traceID string, data []byte, headers messenger.Headers) {
	var cmd domain.LightstripSetSegments
	if err := json.Unmarshal(data, &cmd); err != nil {
		return
	}
	for _, seg := range cmd.Segments {
		idx := seg.ID - 1
		if idx < 0 || idx >= len(strip.Targets) {
			continue
		}
		target := strip.Targets[idx]

		if len(seg.RGB) == 3 {
			rgb, _ := json.Marshal(domain.LightSetRGB{R: seg.RGB[0], G: seg.RGB[1], B: seg.RGB[2]})
			h.appendLog("fanout.published", "info", "virtual segment rgb published", "", "light_set_rgb", traceID, map[string]any{
				"recipient": target,
			})
			outHeaders := messenger.WithOrigin(headers, "sb-virtual", target, "light_set_rgb")
			h.msg.PublishWithHeaders(target+".command.light_set_rgb", rgb, outHeaders)
		}
		if seg.Brightness > 0 {
			br, _ := json.Marshal(domain.LightSetBrightness{Brightness: seg.Brightness})
			h.appendLog("fanout.published", "info", "virtual segment brightness published", "", "light_set_brightness", traceID, map[string]any{
				"recipient": target,
			})
			outHeaders := messenger.WithOrigin(headers, "sb-virtual", target, "light_set_brightness")
			h.msg.PublishWithHeaders(target+".command.light_set_brightness", br, outHeaders)
		}
	}
}

func (h *Handler) appendLog(kind, level, message, entity, action, traceID string, data map[string]any) {
	if h == nil || h.log == nil {
		return
	}
	event := logging.Event{
		ID:      fmt.Sprintf("sb-virtual-%d", atomic.AddUint64(&logSequence, 1)),
		TS:      time.Now().UTC(),
		Source:  "sb-virtual",
		Kind:    kind,
		Level:   level,
		Message: message,
		Entity:  entity,
		Action:  action,
		TraceID: traceID,
		Data:    data,
	}
	if err := h.log.Append(context.Background(), event); err != nil {
		log.Printf("virtual: append log failed: %v", err)
	}
}

type keyStr string

func (k keyStr) Key() string { return string(k) }
