package bridge

import (
	"encoding/json"
	"log/slog"
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

func newTestBridge() (*Bridge, *fakeClient) {
	fc := &fakeClient{}
	return New(fc, "beomqtt", "12345", slog.New(slog.DiscardHandler)), fc
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

func TestVolumePublishesLevelAndMuted(t *testing.T) {
	b, fc := newTestBridge()
	b.HandleNotification(notif(t, "WebSocketEventVolume",
		`{"level":{"level":40},"muted":{"muted":true},"maximum":{"level":100}}`))

	if len(fc.pubs) != 2 {
		t.Fatalf("expected 2 publishes, got %v", fc.pubs)
	}
	if got := fc.pubs[0]; got != (published{"beomqtt/12345/state/volume", true, "40"}) {
		t.Errorf("volume publish = %+v", got)
	}
	if got := fc.pubs[1]; got != (published{"beomqtt/12345/state/muted", true, "true"}) {
		t.Errorf("muted publish = %+v", got)
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

	want := []published{
		{"beomqtt/12345/state/sound_settings", true, `{"adjustments":{"bass":1}}`},
		{"beomqtt/12345/event/beolink_join_result", false, `{"type":"join"}`},
		{"beomqtt/12345/state/progress", false, `{"progress":12}`},
	}
	if len(fc.pubs) != len(want) {
		t.Fatalf("expected %d publishes, got %v", len(want), fc.pubs)
	}
	for i, w := range want {
		if fc.pubs[i] != w {
			t.Errorf("publish %d = %+v, want %+v", i, fc.pubs[i], w)
		}
	}
}

func TestUnknownTypePublishesNothing(t *testing.T) {
	b, fc := newTestBridge()
	b.HandleNotification(notif(t, "WebSocketEventSomethingNew", `{"x":1}`))
	if len(fc.pubs) != 0 {
		t.Errorf("expected no publishes, got %v", fc.pubs)
	}
}

func TestNowPlayingFlattensAndPicksLargestArt(t *testing.T) {
	b, fc := newTestBridge()
	b.HandleNotification(notif(t, "WebSocketEventPlaybackMetadata",
		`{"source":"spotify","artistName":"Artist","title":"Song","albumName":"Album",
		  "art":[{"url":"s.jpg","size":"small"},{"url":"l.jpg","size":"large"},{"url":"m.jpg","size":"medium"}]}`))

	got := fc.single(t)
	if got.topic != "beomqtt/12345/state/nowplaying" || !got.retained {
		t.Fatalf("publish = %+v", got)
	}
	var np NowPlaying
	if err := json.Unmarshal([]byte(got.payload), &np); err != nil {
		t.Fatal(err)
	}
	want := NowPlaying{Source: "spotify", Artist: "Artist", Title: "Song", Album: "Album", ArtURL: "l.jpg"}
	if np != want {
		t.Errorf("nowplaying = %+v, want %+v", np, want)
	}
}
