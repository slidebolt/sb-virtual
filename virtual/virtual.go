// Package virtual provides command fanout for virtual entities.
// A virtual entity has either a Target query (group) or a LightStrip
// with ordered Targets (strip). When a command arrives for a virtual
// entity, the handler resolves the targets and publishes per-member
// commands on the same NATS bus.
package virtual

import (
	"encoding/json"
	"log"
	"strings"

	domain "github.com/slidebolt/sb-domain"
	messenger "github.com/slidebolt/sb-messenger-sdk"
	storage "github.com/slidebolt/sb-storage-sdk"
)

// Handler intercepts commands to virtual entities and fans them out
// to the real member entities.
type Handler struct {
	msg   messenger.Messenger
	store storage.Storage
}

// NewHandler creates a Handler wired to the given messenger and storage.
func NewHandler(msg messenger.Messenger, store storage.Storage) *Handler {
	return &Handler{msg: msg, store: store}
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
			h.fanoutStrip(strip, action, m.Data)
			return
		}
		h.fanoutQuery(entity.Target, action, m.Data)
		return
	}

	strip, ok := entity.State.(domain.LightStrip)
	if !ok || len(strip.Targets) == 0 {
		return
	}
	h.fanoutStrip(strip, action, m.Data)
}

func (h *Handler) fanoutQuery(target json.RawMessage, action string, data []byte) {
	var q storage.Query
	if err := json.Unmarshal(target, &q); err != nil {
		log.Printf("virtual: unmarshal target query: %v", err)
		return
	}
	members, err := h.store.Query(q)
	if err != nil {
		log.Printf("virtual: query members: %v", err)
		return
	}
	for _, member := range members {
		h.msg.Publish(member.Key+".command."+action, data)
	}
}

func (h *Handler) fanoutStrip(strip domain.LightStrip, action string, data []byte) {
	if action == "lightstrip_set_segments" {
		h.fanoutSegments(strip, data)
		return
	}
	for _, target := range strip.Targets {
		h.msg.Publish(target+".command."+action, data)
	}
}

func (h *Handler) fanoutSegments(strip domain.LightStrip, data []byte) {
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
			h.msg.Publish(target+".command.light_set_rgb", rgb)
		}
		if seg.Brightness > 0 {
			br, _ := json.Marshal(domain.LightSetBrightness{Brightness: seg.Brightness})
			h.msg.Publish(target+".command.light_set_brightness", br)
		}
	}
}

type keyStr string

func (k keyStr) Key() string { return string(k) }
