// Package bridge maps Mozart WebSocket notifications onto MQTT topics.
//
// Topic layout (see README for the full reference):
//
//	<prefix>/<device-id>/available            retained  online/offline
//	<prefix>/<device-id>/info                 retained  JSON device identity
//	<prefix>/<device-id>/state/...            retained  last-known state
//	<prefix>/<device-id>/remote/<key...>      event     press/release
//
// State topics are retained so a subscriber that connects later still sees
// the current state; remote button events are moments in time and are not.
//
// Every structured (object-shaped) payload is published in one of two
// forms, controlled by Bridge.Unwrap (BEOMQTT_PAYLOAD_FORMAT), never both:
// a single JSON blob at its topic, or "unwrapped" — each field of the
// decoded JSON republished under its own subtopic (objects become path
// segments, arrays become numeric indices), e.g. state/battery/batteryLevel
// instead of a state/battery JSON blob. Unwrapped is the default, matching
// the one-value-per-topic convention some MQTT ecosystems expect (Homie,
// Zigbee2MQTT attribute mode, Tasmota).
package bridge

import (
	"encoding/json"
	"log/slog"
	"strconv"
	"strings"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"

	"beomqtt/internal/mozartapi"
	"beomqtt/internal/mozartws"
)

const publishTimeout = 10 * time.Second

// DeviceInfo is published retained on the info topic.
type DeviceInfo struct {
	FriendlyName string `json:"friendlyName"`
	Jid          string `json:"jid"`
	Serial       string `json:"serial"`
	Host         string `json:"host"`
}

// Bridge publishes notifications from one Mozart device to MQTT. Safe for
// concurrent use (notifications arrive from two WebSocket goroutines).
type Bridge struct {
	mqtt   mqtt.Client
	base   string // "<prefix>/<device-id>"
	unwrap bool   // true: flattened per-field topics; false: one JSON blob
	log    *slog.Logger
}

func New(client mqtt.Client, prefix, deviceID string, unwrap bool, log *slog.Logger) *Bridge {
	return &Bridge{
		mqtt:   client,
		base:   prefix + "/" + deviceID,
		unwrap: unwrap,
		log:    log,
	}
}

// AvailabilityTopic is where online/offline is published; main registers
// the MQTT last-will on the same topic so the broker reports offline if
// the bridge itself dies.
func (b *Bridge) AvailabilityTopic() string { return b.base + "/available" }

func (b *Bridge) PublishAvailability(online bool) {
	b.publish(b.AvailabilityTopic(), availabilityPayload(online), true)
}

// PublishAvailabilitySync blocks until the availability publish completes.
// For the shutdown path: the final offline must be on the wire before the
// MQTT disconnect, or the graceful disconnect races the publish and — the
// will being suppressed on clean disconnects — the topic stays "online"
// forever.
func (b *Bridge) PublishAvailabilitySync(online bool) {
	token := b.mqtt.Publish(b.AvailabilityTopic(), 1, true, []byte(availabilityPayload(online)))
	if !token.WaitTimeout(publishTimeout) {
		b.log.Warn("availability publish timed out", "online", online)
		return
	}
	if err := token.Error(); err != nil {
		b.log.Warn("availability publish failed", "online", online, "error", err)
	}
}

func availabilityPayload(online bool) string {
	if online {
		return "online"
	}
	return "offline"
}

func (b *Bridge) PublishInfo(info DeviceInfo) {
	b.publishJSON(b.base+"/info", info, true)
}

// passthrough events republish eventData as raw JSON: their payloads are
// already flat, documented Mozart schemas, and inventing our own shapes
// for the long tail would just be a second thing to document. Retained
// entries are device state; the rest are moments in time.
type passthrough struct {
	topic    string
	retained bool
}

var passthroughTopics = map[string]passthrough{
	// State-like: retained under state/.
	"WebSocketEventActiveHdmiInputSignal":    {"state/active_hdmi_input_signal", true},
	"WebSocketEventActiveListeningMode":      {"state/listening_mode", true},
	"WebSocketEventActiveSpeakerGroup":       {"state/speaker_group", true},
	"WebSocketEventAlarmTimer":               {"state/alarm_timer", true},
	"WebSocketEventChannelSurveyStatus":      {"state/channel_survey_status", true},
	"WebSocketEventClassicsAdapterContent":   {"state/classics_adapter_content", true},
	"WebSocketEventCurtains":                 {"state/curtains", true},
	"WebSocketEventHdmiVideoFormatSignal":    {"state/hdmi_video_format_signal", true},
	"WebSocketEventPlaybackSource":           {"state/playback_source", true},
	"WebSocketEventPowerlinkConnectionState": {"state/powerlink_connection_state", true},
	"WebSocketEventPucInstallRemoteIdStatus": {"state/puc_install_remote_id_status", true},
	"WebSocketEventRoomCompensationState":    {"state/room_compensation_state", true},
	"WebSocketEventSoftwareUpdateState":      {"state/software_update_state", true},
	"WebSocketEventSoundSettings":            {"state/sound_settings", true},
	"WebSocketEventSpeakerGroupChanged":      {"state/speaker_group_config", true},
	"WebSocketEventSpeakerLinkStatusChanged": {"state/speaker_link_status", true},
	"WebSocketEventStandConnected":           {"state/stand_connected", true},
	"WebSocketEventStandPosition":            {"state/stand_position", true},
	"WebSocketEventTvBnOMode":                {"state/tv_bno_mode", true},
	"WebSocketEventTvInfo":                   {"state/tv_info", true},
	"WebSocketEventWisaOutState":             {"state/wisa_out_state", true},

	// Progress ticks every second during playback; retaining a stale
	// position would be misleading, so it is the one state topic that
	// is not retained.
	"WebSocketEventPlaybackProgress": {"state/progress", false},

	// Momentary: not retained, under event/.
	"WebSocketEventAlarmTriggered":                          {"event/alarm_triggered", false},
	"WebSocketEventBeolinkExperiencesResult":                {"event/beolink_experiences_result", false},
	"WebSocketEventBeolinkJoinResult":                       {"event/beolink_join_result", false},
	"WebSocketEventNotification":                            {"event/notification", false},
	"WebSocketEventPlaybackError":                           {"event/playback_error", false},
	"WebSocketEventRoomCompensationCurrentMeasurementEvent": {"event/room_compensation_measurement", false},
}

// HandleNotification is the mozartws.Client callback.
func (b *Bridge) HandleNotification(n mozartws.Notification) {
	switch n.EventType {
	case "WebSocketEventPlaybackState":
		b.publishValue("state/playback", n)

	case "WebSocketEventPowerState":
		b.publishValue("state/power", n)

	case "WebSocketEventRole":
		b.publishValue("state/role", n)

	case "WebSocketEventVolume":
		// state/volume and state/muted are convenience scalars (the
		// values almost every consumer wants); VolumeState also carries
		// default/maximum levels that those two don't surface, so the
		// full object is published too, under its own topic rather than
		// silently dropped.
		var v mozartapi.VolumeState
		if !b.decode(n, &v) {
			return
		}
		if v.Level != nil && v.Level.Level != nil {
			b.publish(b.base+"/state/volume", int(*v.Level.Level), true)
		}
		if v.Muted != nil && v.Muted.Muted != nil {
			b.publish(b.base+"/state/muted", *v.Muted.Muted, true)
		}
		b.publishStructured(b.base+"/state/volume_state", n.EventData, true)

	case "WebSocketEventSourceChange":
		// state/source is the convenience scalar (the source id); Source
		// also carries name/type/capability flags that it doesn't
		// surface, published in full under their own topic instead.
		var s mozartapi.Source
		if !b.decode(n, &s) {
			return
		}
		if s.Id != nil {
			b.publish(b.base+"/state/source", *s.Id, true)
		}
		b.publishStructured(b.base+"/state/source_state", n.EventData, true)

	case "WebSocketEventPlaybackMetadata":
		// Published as the full raw Mozart payload (all 22
		// PlaybackContentMetadata fields, not a hand-picked subset) plus
		// one convenience field: many sources (TV/HDMI passthrough in
		// particular) only ever change fields like id/queueId/track
		// between events, so curating down to a handful of "interesting"
		// fields silently hid the very field that changed.
		var fields map[string]any
		if !b.decode(n, &fields) {
			return
		}
		if artURL := largestArtURL(fields["art"]); artURL != "" {
			fields["artUrl"] = artURL
		}
		b.publishJSON(b.base+"/state/nowplaying", fields, true)

	case "WebSocketEventBattery":
		b.publishStructured(b.base+"/state/battery", n.EventData, true)

	case "WebSocketEventBeoRemoteButton":
		var r mozartapi.BeoRemoteButton
		if !b.decode(n, &r) {
			return
		}
		b.publishRemoteButton(r)

	case "WebSocketEventButton":
		// The device's own physical buttons, analogous to remote/ but
		// with Mozart's own state vocabulary (shortPress, longPress,
		// released, ...) passed through as the payload.
		var e mozartapi.ButtonEvent
		if !b.decode(n, &e) {
			return
		}
		if e.Button == nil || e.State == nil {
			return
		}
		b.publish(b.base+"/button/"+strings.ToLower(*e.Button), *e.State, false)

	default:
		if pt, ok := passthroughTopics[n.EventType]; ok {
			b.publishStructured(b.base+"/"+pt.topic, n.EventData, pt.retained)
			return
		}
		// A type we don't know: either a spec update or a device
		// speaking a newer API than our vendored spec.
		b.log.Debug("unmapped notification", "type", n.EventType)
	}
}

// publishValue handles the family of payloads shaped {"value": "..."}.
func (b *Bridge) publishValue(subtopic string, n mozartws.Notification) {
	var p struct {
		Value string `json:"value"`
	}
	if !b.decode(n, &p) || p.Value == "" {
		return
	}
	b.publish(b.base+"/"+subtopic, p.Value, true)
}

// publishRemoteButton turns a Beoremote One key path like "Control/Play"
// into remote/control/play with payload press/release.
func (b *Bridge) publishRemoteButton(r mozartapi.BeoRemoteButton) {
	if r.Key == nil || r.Type == nil {
		// Silently dropping this made "why don't I see any remote
		// events" indistinguishable from "the notification never
		// arrived" (see: the Beoremote One only emits these while
		// navigated into its Control or Light submenu — nothing to do
		// with this code path, but worth being loud so it isn't
		// mistaken for one).
		b.log.Warn("BeoRemoteButton notification missing key or type", "key", r.Key, "type", r.Type)
		return
	}
	var payload string
	switch *r.Type {
	case "KeyPress":
		payload = "press"
	case "KeyRelease":
		payload = "release"
	default:
		b.log.Warn("unknown remote button event type", "type", *r.Type)
		return
	}
	topic := b.base + "/remote/" + strings.ToLower(strings.Trim(*r.Key, "/"))
	b.publish(topic, payload, false)
}

// largestArtURL picks the highest-resolution artwork URL out of a decoded
// PlaybackContentMetadata "art" array (each entry has url/size) as a
// convenience for consumers that just want one image. The full art array
// is still published in full — this only adds a field, never removes one.
func largestArtURL(art any) string {
	entries, ok := art.([]any)
	if !ok {
		return ""
	}
	bySize := map[string]string{}
	first := ""
	for _, e := range entries {
		entry, ok := e.(map[string]any)
		if !ok {
			continue
		}
		url, _ := entry["url"].(string)
		size, _ := entry["size"].(string)
		if url == "" {
			continue
		}
		if first == "" {
			first = url
		}
		if size != "" {
			bySize[size] = url
		}
	}
	for _, size := range []string{"large", "medium", "small"} {
		if url, ok := bySize[size]; ok {
			return url
		}
	}
	return first
}

func (b *Bridge) decode(n mozartws.Notification, into any) bool {
	if err := json.Unmarshal(n.EventData, into); err != nil {
		b.log.Warn("undecodable notification payload", "type", n.EventType, "error", err)
		return false
	}
	return true
}

func (b *Bridge) publishJSON(topic string, v any, retained bool) {
	data, err := json.Marshal(v)
	if err != nil {
		b.log.Error("marshal payload", "topic", topic, "error", err)
		return
	}
	b.publishStructured(topic, data, retained)
}

// publishStructured emits a structured (JSON-object) payload in whichever
// form Bridge.unwrap selects — see package doc. Never both: doubling every
// structured topic's publish volume for a redundant representation isn't
// worth it when subscribers only ever want one or the other.
func (b *Bridge) publishStructured(topic string, raw json.RawMessage, retained bool) {
	if !b.unwrap {
		b.publish(topic, string(raw), retained)
		return
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		b.log.Warn("cannot decode payload", "topic", topic, "error", err)
		return
	}
	b.flatten(topic, v, retained)
}

func (b *Bridge) flatten(topic string, v any, retained bool) {
	switch val := v.(type) {
	case map[string]any:
		for k, child := range val {
			b.flatten(topic+"/"+k, child, retained)
		}
	case []any:
		for i, child := range val {
			b.flatten(topic+"/"+strconv.Itoa(i), child, retained)
		}
	case nil:
		// Omit: an empty retained topic would look like real data to a
		// subscriber rather than "field absent".
	default:
		b.publish(topic, val, retained)
	}
}

// publish fires QoS 1 and reports failures asynchronously: WS handler
// goroutines must not block on a slow broker (paho queues and retries
// while reconnecting).
func (b *Bridge) publish(topic string, payload any, retained bool) {
	token := b.mqtt.Publish(topic, 1, retained, toBytes(payload))
	go func() {
		if !token.WaitTimeout(publishTimeout) {
			b.log.Warn("publish timed out", "topic", topic)
			return
		}
		if err := token.Error(); err != nil {
			b.log.Warn("publish failed", "topic", topic, "error", err)
		}
	}()
}

func toBytes(payload any) []byte {
	switch p := payload.(type) {
	case string:
		return []byte(p)
	case []byte:
		return p
	default:
		data, _ := json.Marshal(p)
		return data
	}
}
