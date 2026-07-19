// Package mozartws is a hand-written WebSocket client for the Mozart
// platform's notification channel.
//
// Mozart devices push state notifications over ws://<host>:9339/. Beoremote
// One button events are not part of that default stream: they arrive on a
// second, dedicated endpoint at ws://<host>:9339/remoteControl (this is how
// the official Python client's remote_control=True flag is realized on the
// wire — a separate connection, not a query parameter).
//
// Every message on either socket is a JSON envelope:
//
//	{"eventType": "WebSocketEventVolume", "eventData": {...}}
//
// where eventType names a WebSocketEvent* schema from the Mozart OpenAPI
// spec and eventData is the payload (decodable into the corresponding
// model in internal/mozartapi).
package mozartws

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/coder/websocket"
)

const (
	// NotificationPort is the WebSocket port on Mozart devices. The REST
	// API lives on plain HTTP port 80; notifications are separate.
	NotificationPort = 9339

	dialTimeout  = 5 * time.Second
	pingInterval = 15 * time.Second
	pingTimeout  = 5 * time.Second
	backoffMin   = time.Second
	backoffMax   = time.Minute
	// A connection that stays up at least this long resets the backoff,
	// so a device that flaps every few minutes still reconnects quickly.
	backoffResetAfter = 30 * time.Second

	// Now-playing metadata can be sizable; the library default (32 KiB)
	// is too tight to trust.
	readLimit = 1 << 20
)

// Notification is one decoded envelope from the notification stream.
type Notification struct {
	EventType string          `json:"eventType"`
	EventData json.RawMessage `json:"eventData"`
}

// Client maintains the WebSocket connection(s) to a single Mozart device
// and dispatches incoming notifications. Configure the exported fields
// before calling Run; they must not be modified afterwards.
type Client struct {
	// Host is the device's IP or hostname (no port).
	Host string

	// RemoteControl also opens the /remoteControl endpoint for Beoremote
	// One button events.
	RemoteControl bool

	// OnNotification is called for every decoded notification, from the
	// goroutine of whichever endpoint received it. It must be safe for
	// concurrent use when RemoteControl is enabled.
	OnNotification func(n Notification)

	// OnConnect/OnDisconnect observe connection state of the main
	// notification endpoint (the /remoteControl socket intentionally does
	// not trigger these: device availability is defined by the primary
	// stream). Either may be nil.
	OnConnect    func()
	OnDisconnect func(err error)

	// Logger must be non-nil.
	Logger *slog.Logger
}

// Run connects to the device and keeps the connection(s) alive, redialing
// with exponential backoff on any failure, until ctx is cancelled. It only
// returns ctx.Err(): connection failures are retried forever, because a
// Mozart device being temporarily offline is a normal condition, not a
// reason to give up.
func (c *Client) Run(ctx context.Context) error {
	endpoints := []string{"/"}
	if c.RemoteControl {
		endpoints = append(endpoints, "/remoteControl")
	}

	var wg sync.WaitGroup
	for _, path := range endpoints {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.runEndpoint(ctx, path)
		}()
	}
	wg.Wait()
	return ctx.Err()
}

// runEndpoint is the dial/read/backoff loop for one WebSocket path.
func (c *Client) runEndpoint(ctx context.Context, path string) {
	url := fmt.Sprintf("ws://%s:%d%s", c.Host, NotificationPort, path)
	log := c.Logger.With("device", c.Host, "endpoint", path)
	backoff := backoffMin

	for {
		connectedAt := time.Now()
		err := c.serveConn(ctx, url, path, log)
		if ctx.Err() != nil {
			return
		}
		if time.Since(connectedAt) >= backoffResetAfter {
			backoff = backoffMin
		}
		// Full jitter: sleeping anywhere in (0, backoff] avoids all
		// devices redialing in lockstep after e.g. a network blip.
		delay := rand.N(backoff) + time.Millisecond
		log.Warn("connection lost, reconnecting", "error", err, "retry_in", delay.Round(time.Millisecond))
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
		backoff = min(backoff*2, backoffMax)
	}
}

// serveConn dials once and reads until the connection or ctx dies.
func (c *Client) serveConn(ctx context.Context, url, path string, log *slog.Logger) error {
	dialCtx, cancel := context.WithTimeout(ctx, dialTimeout)
	conn, _, err := websocket.Dial(dialCtx, url, nil)
	cancel()
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "shutting down")
	conn.SetReadLimit(readLimit)

	log.Info("connected")
	primary := path == "/"
	if primary && c.OnConnect != nil {
		c.OnConnect()
	}
	readErr := c.readLoop(ctx, conn, log)
	if primary && c.OnDisconnect != nil {
		c.OnDisconnect(readErr)
	}
	return readErr
}

func (c *Client) readLoop(ctx context.Context, conn *websocket.Conn, log *slog.Logger) error {
	// The device does not ping us, so probe liveness ourselves: a dead
	// TCP path otherwise leaves Read blocked for many minutes before the
	// kernel gives up.
	pingCtx, stopPings := context.WithCancel(ctx)
	defer stopPings()
	pingErr := make(chan error, 1)
	go func() {
		ticker := time.NewTicker(pingInterval)
		defer ticker.Stop()
		for {
			select {
			case <-pingCtx.Done():
				return
			case <-ticker.C:
				pctx, cancel := context.WithTimeout(pingCtx, pingTimeout)
				err := conn.Ping(pctx)
				cancel()
				if err != nil {
					if pingCtx.Err() == nil {
						pingErr <- fmt.Errorf("ping: %w", err)
					}
					return
				}
			}
		}
	}()

	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			// A ping failure closes the connection, which surfaces
			// here as a read error; prefer reporting the root cause.
			select {
			case perr := <-pingErr:
				return perr
			default:
			}
			return fmt.Errorf("read: %w", err)
		}

		var n Notification
		if err := json.Unmarshal(data, &n); err != nil || n.EventType == "" {
			if err == nil {
				err = errors.New("missing eventType")
			}
			log.Warn("discarding undecodable notification", "error", err, "payload", truncate(data, 256))
			continue
		}
		if c.OnNotification != nil {
			c.OnNotification(n)
		}
	}
}

func truncate(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "…"
}
