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
package bridge

import (
	"encoding/json"
	"log/slog"
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

// NowPlaying is the flattened now-playing shape published on
// state/nowplaying. Fields mirror the useful subset of the Mozart
// PlaybackContentMetadata payload.
type NowPlaying struct {
	Source       string `json:"source,omitempty"`
	Artist       string `json:"artist,omitempty"`
	Title        string `json:"title,omitempty"`
	Album        string `json:"album,omitempty"`
	Genre        string `json:"genre,omitempty"`
	Organization string `json:"organization,omitempty"`
	ArtURL       string `json:"artUrl,omitempty"`
}

// Bridge publishes notifications from one Mozart device to MQTT. Safe for
// concurrent use (notifications arrive from two WebSocket goroutines).
type Bridge struct {
	mqtt mqtt.Client
	base string // "<prefix>/<device-id>"
	log  *slog.Logger
}

func New(client mqtt.Client, prefix, deviceID string, log *slog.Logger) *Bridge {
	return &Bridge{
		mqtt: client,
		base: prefix + "/" + deviceID,
		log:  log,
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

	case "WebSocketEventSourceChange":
		var s mozartapi.Source
		if !b.decode(n, &s) {
			return
		}
		if s.Id != nil {
			b.publish(b.base+"/state/source", *s.Id, true)
		}

	case "WebSocketEventPlaybackMetadata":
		var m mozartapi.PlaybackContentMetadata
		if !b.decode(n, &m) {
			return
		}
		b.publishJSON(b.base+"/state/nowplaying", nowPlayingFrom(m), true)

	case "WebSocketEventBattery":
		// The payload (BatteryState) is already flat and documented;
		// republish as-is.
		b.publish(b.base+"/state/battery", string(n.EventData), true)

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
			b.publish(b.base+"/"+pt.topic, string(n.EventData), pt.retained)
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

func nowPlayingFrom(m mozartapi.PlaybackContentMetadata) NowPlaying {
	np := NowPlaying{}
	if m.Source != nil {
		np.Source = *m.Source
	}
	if m.ArtistName != nil {
		np.Artist = *m.ArtistName
	}
	if m.Title != nil {
		np.Title = *m.Title
	}
	if m.AlbumName != nil {
		np.Album = *m.AlbumName
	}
	if m.Genre != nil {
		np.Genre = *m.Genre
	}
	if m.Organization != nil {
		np.Organization = *m.Organization
	}
	// Prefer the largest artwork; entries are typically small/medium/large.
	for _, size := range []string{"large", "medium", "small"} {
		for _, art := range m.Art {
			if art.Url != nil && art.Size != nil && *art.Size == size {
				np.ArtURL = *art.Url
				return np
			}
		}
	}
	for _, art := range m.Art {
		if art.Url != nil {
			np.ArtURL = *art.Url
			break
		}
	}
	return np
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
	b.publish(topic, string(data), retained)
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
