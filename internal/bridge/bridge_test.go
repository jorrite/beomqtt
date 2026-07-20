package bridge

import (
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"

	"beomqtt/internal/mozartws"
)

// fakeToken satisfies mqtt.Token with an already-completed publish.
type fakeToken struct{ done chan struct{} }

func newFakeToken() *fakeToken {
	t := &fakeToken{done: make(chan struct{})}
	close(t.done)
	return t
}

func (t *fakeToken) Wait() bool                     { return true }
func (t *fakeToken) WaitTimeout(time.Duration) bool { return true }
func (t *fakeToken) Done() <-chan struct{}          { return t.done }
func (t *fakeToken) Error() error                   { return nil }

type published struct {
	topic    string
	retained bool
	payload  string
}

// fakeClient records Publish calls; every other mqtt.Client method is a
// stub.
type fakeClient struct {
	mu   sync.Mutex
	pubs []published
}

func (f *fakeClient) Publish(topic string, _ byte, retained bool, payload any) mqtt.Token {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pubs = append(f.pubs, published{topic, retained, string(payload.([]byte))})
	return newFakeToken()
}

func (f *fakeClient) IsConnected() bool      { return true }
func (f *fakeClient) IsConnectionOpen() bool { return true }
func (f *fakeClient) Connect() mqtt.Token    { return newFakeToken() }
func (f *fakeClient) Disconnect(uint)        {}
func (f *fakeClient) Subscribe(string, byte, mqtt.MessageHandler) mqtt.Token {
	return newFakeToken()
}
func (f *fakeClient) SubscribeMultiple(map[string]byte, mqtt.MessageHandler) mqtt.Token {
	return newFakeToken()
}
func (f *fakeClient) Unsubscribe(...string) mqtt.Token        { return newFakeToken() }
func (f *fakeClient) AddRoute(string, mqtt.MessageHandler)    {}
func (f *fakeClient) OptionsReader() mqtt.ClientOptionsReader { return mqtt.ClientOptionsReader{} }

// newTestBridge builds a bridge in the default (unwrap/flattened) mode.
func newTestBridge() (*Bridge, *fakeClient) {
	return newTestBridgeMode(true)
}

// newTestBridgeJSON builds a bridge in BEOMQTT_PAYLOAD_FORMAT=json mode.
func newTestBridgeJSON() (*Bridge, *fakeClient) {
	return newTestBridgeMode(false)
}

func newTestBridgeMode(unwrap bool) (*Bridge, *fakeClient) {
	fc := &fakeClient{}
	return New(fc, "beomqtt", "12345", unwrap, slog.New(slog.DiscardHandler)), fc
}

func notif(t *testing.T, eventType, data string) mozartws.Notification {
	t.Helper()
	return mozartws.Notification{EventType: eventType, EventData: json.RawMessage(data)}
}

func (f *fakeClient) single(t *testing.T) published {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.pubs) != 1 {
		t.Fatalf("expected exactly 1 publish, got %d: %v", len(f.pubs), f.pubs)
	}
	return f.pubs[0]
}

// byTopic looks up a publish by exact topic. Needed for anything that goes
// through flatten(), since map iteration order (and therefore publish
// order) is not guaranteed.
func (f *fakeClient) byTopic(t *testing.T, topic string) published {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, p := range f.pubs {
		if p.topic == topic {
			return p
		}
	}
	t.Fatalf("no publish to topic %q; got %v", topic, f.pubs)
	return published{}
}

// requireNoChildTopics fails if any publish landed under topic+"/" —
// the json-mode invariant that a blob topic never also grows leaves.
func (f *fakeClient) requireNoChildTopics(t *testing.T, topic string) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	prefix := topic + "/"
	for _, p := range f.pubs {
		if strings.HasPrefix(p.topic, prefix) {
			t.Errorf("unexpected child topic %q under blob-only topic %q", p.topic, topic)
		}
	}
}

func TestVolumePublishesLevelAndMuted(t *testing.T) {
	b, fc := newTestBridge()
	b.HandleNotification(notif(t, "WebSocketEventVolume",
		`{"level":{"level":40},"muted":{"muted":true},"maximum":{"level":100}}`))

	if got := fc.byTopic(t, "beomqtt/12345/state/volume"); got != (published{"beomqtt/12345/state/volume", true, "40"}) {
		t.Errorf("volume publish = %+v", got)
	}
	if got := fc.byTopic(t, "beomqtt/12345/state/muted"); got != (published{"beomqtt/12345/state/muted", true, "true"}) {
		t.Errorf("muted publish = %+v", got)
	}
}

// TestVolumeStateNotDropped guards the bug this project actually shipped:
// VolumeState carries default/level/maximum/muted, but state/volume and
// state/muted only ever surfaced level and muted. default and maximum
// were silently dropped rather than published anywhere. They now live
// under state/volume_state, flattened like any other structured topic.
func TestVolumeStateNotDropped(t *testing.T) {
	b, fc := newTestBridge()
	b.HandleNotification(notif(t, "WebSocketEventVolume",
		`{"default":{"level":20},"level":{"level":40},"maximum":{"level":100},"muted":{"muted":false}}`))

	for topic, want := range map[string]string{
		"beomqtt/12345/state/volume_state/default/level": "20",
		"beomqtt/12345/state/volume_state/level/level":   "40",
		"beomqtt/12345/state/volume_state/maximum/level": "100",
		"beomqtt/12345/state/volume_state/muted/muted":   "false",
	} {
		if got := fc.byTopic(t, topic).payload; got != want {
			t.Errorf("%s = %q, want %q", topic, got, want)
		}
	}
}

// TestSourceStateNotDropped guards the second field-dropping bug: Source
// carries name/type/isEnabled/isMultiroomAvailable/isPlayable/isSeekable,
// but state/source only ever surfaced id. The rest now live under
// state/source_state.
func TestSourceStateNotDropped(t *testing.T) {
	b, fc := newTestBridge()
	b.HandleNotification(notif(t, "WebSocketEventSourceChange",
		`{"id":"tv","name":"Apple TV","type":{"value":"tv"},"isEnabled":true,"isPlayable":true,"isSeekable":false}`))

	if got := fc.byTopic(t, "beomqtt/12345/state/source"); got != (published{"beomqtt/12345/state/source", true, "tv"}) {
		t.Errorf("source publish = %+v", got)
	}
	for topic, want := range map[string]string{
		"beomqtt/12345/state/source_state/name":       "Apple TV",
		"beomqtt/12345/state/source_state/type/value": "tv",
		"beomqtt/12345/state/source_state/isEnabled":  "true",
		"beomqtt/12345/state/source_state/isPlayable": "true",
		"beomqtt/12345/state/source_state/isSeekable": "false",
	} {
		if got := fc.byTopic(t, topic).payload; got != want {
			t.Errorf("%s = %q, want %q", topic, got, want)
		}
	}
}

func TestPlaybackStateValue(t *testing.T) {
	b, fc := newTestBridge()
	b.HandleNotification(notif(t, "WebSocketEventPlaybackState", `{"value":"paused"}`))
	if got := fc.single(t); got != (published{"beomqtt/12345/state/playback", true, "paused"}) {
		t.Errorf("publish = %+v", got)
	}
}

func TestRemoteButtonTopicAndPayload(t *testing.T) {
	b, fc := newTestBridge()
	b.HandleNotification(notif(t, "WebSocketEventBeoRemoteButton",
		`{"Key":"Control/Play","Type":"KeyPress"}`))
	if got := fc.single(t); got != (published{"beomqtt/12345/remote/control/play", false, "press"}) {
		t.Errorf("publish = %+v", got)
	}
}

func TestDeviceButtonPassesStateThrough(t *testing.T) {
	b, fc := newTestBridge()
	b.HandleNotification(notif(t, "WebSocketEventButton",
		`{"button":"PlayPause","state":"shortPress"}`))
	if got := fc.single(t); got != (published{"beomqtt/12345/button/playpause", false, "shortPress"}) {
		t.Errorf("publish = %+v", got)
	}
}

func TestPassthroughRetainedAndEvent(t *testing.T) {
	b, fc := newTestBridge()
	b.HandleNotification(notif(t, "WebSocketEventSoundSettings", `{"adjustments":{"bass":1}}`))
	b.HandleNotification(notif(t, "WebSocketEventBeolinkJoinResult", `{"type":"join"}`))
	b.HandleNotification(notif(t, "WebSocketEventPlaybackProgress", `{"progress":12}`))

	// Default mode (flattened): leaves only, no blob.
	if got := fc.byTopic(t, "beomqtt/12345/state/sound_settings/adjustments/bass"); got != (published{"beomqtt/12345/state/sound_settings/adjustments/bass", true, "1"}) {
		t.Errorf("sound_settings leaf = %+v", got)
	}
	if got := fc.byTopic(t, "beomqtt/12345/event/beolink_join_result/type"); got != (published{"beomqtt/12345/event/beolink_join_result/type", false, "join"}) {
		t.Errorf("beolink_join_result leaf = %+v", got)
	}
	if got := fc.byTopic(t, "beomqtt/12345/state/progress/progress"); got != (published{"beomqtt/12345/state/progress/progress", false, "12"}) {
		t.Errorf("progress leaf = %+v", got)
	}
	if len(fc.pubs) != 3 {
		t.Errorf("expected 3 leaf publishes (one per notification, no blobs), got %d: %v", len(fc.pubs), fc.pubs)
	}
}

func TestPassthroughJSONModePublishesBlobOnly(t *testing.T) {
	b, fc := newTestBridgeJSON()
	b.HandleNotification(notif(t, "WebSocketEventSoundSettings", `{"adjustments":{"bass":1}}`))
	b.HandleNotification(notif(t, "WebSocketEventBeolinkJoinResult", `{"type":"join"}`))

	if got := fc.byTopic(t, "beomqtt/12345/state/sound_settings"); got != (published{"beomqtt/12345/state/sound_settings", true, `{"adjustments":{"bass":1}}`}) {
		t.Errorf("sound_settings blob = %+v", got)
	}
	fc.requireNoChildTopics(t, "beomqtt/12345/state/sound_settings")
	if got := fc.byTopic(t, "beomqtt/12345/event/beolink_join_result"); got != (published{"beomqtt/12345/event/beolink_join_result", false, `{"type":"join"}`}) {
		t.Errorf("beolink_join_result blob = %+v", got)
	}
	fc.requireNoChildTopics(t, "beomqtt/12345/event/beolink_join_result")
	if len(fc.pubs) != 2 {
		t.Errorf("expected 2 blob publishes (one per notification, no leaves), got %d: %v", len(fc.pubs), fc.pubs)
	}
}

func TestBatteryFlattenedByDefault(t *testing.T) {
	b, fc := newTestBridge()
	b.HandleNotification(notif(t, "WebSocketEventBattery",
		`{"batteryLevel":0,"isCharging":false,"state":"BatteryNotPresent"}`))

	if got := fc.byTopic(t, "beomqtt/12345/state/battery/batteryLevel"); got != (published{"beomqtt/12345/state/battery/batteryLevel", true, "0"}) {
		t.Errorf("batteryLevel leaf = %+v", got)
	}
	if got := fc.byTopic(t, "beomqtt/12345/state/battery/isCharging"); got != (published{"beomqtt/12345/state/battery/isCharging", true, "false"}) {
		t.Errorf("isCharging leaf = %+v", got)
	}
	if got := fc.byTopic(t, "beomqtt/12345/state/battery/state"); got != (published{"beomqtt/12345/state/battery/state", true, "BatteryNotPresent"}) {
		t.Errorf("state leaf = %+v", got)
	}
	// 3 leaves and nothing else — in particular, no blob at the parent
	// topic itself.
	if len(fc.pubs) != 3 {
		t.Errorf("expected exactly 3 leaves, no blob, got %d: %v", len(fc.pubs), fc.pubs)
	}
}

func TestBatteryJSONModePublishesBlobOnly(t *testing.T) {
	b, fc := newTestBridgeJSON()
	b.HandleNotification(notif(t, "WebSocketEventBattery",
		`{"batteryLevel":0,"isCharging":false,"state":"BatteryNotPresent"}`))

	if got := fc.single(t); got != (published{"beomqtt/12345/state/battery", true, `{"batteryLevel":0,"isCharging":false,"state":"BatteryNotPresent"}`}) {
		t.Errorf("battery blob = %+v", got)
	}
}

func TestFlattenNestedObjectsArraysAndNulls(t *testing.T) {
	b, fc := newTestBridge()
	b.flatten("t", map[string]any{
		"a": map[string]any{"b": "c"},
		"d": []any{"x", "y"},
		"e": nil,
		"f": 42.0,
	}, true)

	if got := fc.byTopic(t, "t/a/b"); got.payload != "c" {
		t.Errorf("t/a/b = %+v", got)
	}
	if got := fc.byTopic(t, "t/d/0"); got.payload != "x" {
		t.Errorf("t/d/0 = %+v", got)
	}
	if got := fc.byTopic(t, "t/d/1"); got.payload != "y" {
		t.Errorf("t/d/1 = %+v", got)
	}
	if got := fc.byTopic(t, "t/f"); got.payload != "42" {
		t.Errorf("t/f = %+v", got)
	}
	for _, p := range fc.pubs {
		if p.topic == "t/e" {
			t.Errorf("null field should not publish anything, got %+v", p)
		}
	}
	if len(fc.pubs) != 4 {
		t.Errorf("expected 4 leaf publishes, got %d: %v", len(fc.pubs), fc.pubs)
	}
}

func TestUnknownTypePublishesNothing(t *testing.T) {
	b, fc := newTestBridge()
	b.HandleNotification(notif(t, "WebSocketEventSomethingNew", `{"x":1}`))
	if len(fc.pubs) != 0 {
		t.Errorf("expected no publishes, got %v", fc.pubs)
	}
}

func TestNowPlayingPassesThroughAllFieldsAndAddsArtURL(t *testing.T) {
	b, fc := newTestBridge()
	// queueId is deliberately not one of the "curated" fields this
	// topic used to hand-pick — it must still come through, since a
	// source can change it (or id/track/etc.) without touching any of
	// the fields we used to special-case.
	b.HandleNotification(notif(t, "WebSocketEventPlaybackMetadata",
		`{"source":"spotify","artistName":"Artist","title":"Song","albumName":"Album","queueId":"q1",
		  "art":[{"url":"s.jpg","size":"small"},{"url":"l.jpg","size":"large"},{"url":"m.jpg","size":"medium"}]}`))

	// Default mode (flattened): every raw field lands as its own leaf,
	// including ones the old curated NowPlaying struct used to drop, and
	// the raw art array survives alongside the derived artUrl.
	if got := fc.byTopic(t, "beomqtt/12345/state/nowplaying/queueId"); got.payload != "q1" {
		t.Errorf("state/nowplaying/queueId = %+v", got)
	}
	if got := fc.byTopic(t, "beomqtt/12345/state/nowplaying/artUrl"); got.payload != "l.jpg" {
		t.Errorf("state/nowplaying/artUrl = %+v", got)
	}
	if got := fc.byTopic(t, "beomqtt/12345/state/nowplaying/art/1/url"); got.payload != "l.jpg" {
		t.Errorf("state/nowplaying/art/1/url = %+v", got)
	}
	if got := fc.byTopic(t, "beomqtt/12345/state/nowplaying/source"); !got.retained || got.payload != "spotify" {
		t.Errorf("state/nowplaying/source = %+v", got)
	}
}

func TestNowPlayingJSONModePublishesFullBlobOnly(t *testing.T) {
	b, fc := newTestBridgeJSON()
	b.HandleNotification(notif(t, "WebSocketEventPlaybackMetadata",
		`{"source":"spotify","queueId":"q1","art":[{"url":"l.jpg","size":"large"}]}`))

	blob := fc.single(t)
	if !blob.retained {
		t.Fatalf("nowplaying blob not retained: %+v", blob)
	}
	var fields map[string]any
	if err := json.Unmarshal([]byte(blob.payload), &fields); err != nil {
		t.Fatal(err)
	}
	if fields["queueId"] != "q1" {
		t.Errorf("queueId not passed through: %+v", fields)
	}
	if fields["artUrl"] != "l.jpg" {
		t.Errorf("artUrl not derived: %+v", fields)
	}
	if fields["art"] == nil {
		t.Errorf("raw art array should still be present alongside artUrl: %+v", fields)
	}
}

func TestLargestArtURLPrefersLargeThenFallsBackToFirst(t *testing.T) {
	tests := []struct {
		name string
		art  any
		want string
	}{
		{"picks large among mixed sizes", []any{
			map[string]any{"url": "s.jpg", "size": "small"},
			map[string]any{"url": "l.jpg", "size": "large"},
		}, "l.jpg"},
		{"falls back to first entry when no size matches", []any{
			map[string]any{"url": "only.jpg", "size": "weird"},
		}, "only.jpg"},
		{"not an array", "nope", ""},
		{"nil", nil, ""},
	}
	for _, tt := range tests {
		if got := largestArtURL(tt.art); got != tt.want {
			t.Errorf("%s: largestArtURL(%v) = %q, want %q", tt.name, tt.art, got, tt.want)
		}
	}
}
