// Command beomqtt bridges one Bang & Olufsen Mozart-platform device to
// MQTT: state and events from the device's WebSocket notification channel
// become retained topics and momentary events on the broker.
//
// One process, one device, one broker — run more instances for more
// devices. Configuration is environment-only; see internal/config.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"

	"beomqtt/internal/bridge"
	"beomqtt/internal/config"
	"beomqtt/internal/mozartapi"
	"beomqtt/internal/mozartws"
)

const identifyRetryMax = time.Minute

func main() {
	if err := run(); err != nil {
		slog.Error("fatal error", "error", err)
		os.Exit(1)
	}
}

func run() error {
	printBanner(os.Stderr)

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: cfg.LogLevel}))
	slog.SetDefault(logger)
	logger.Info("starting", "version", version, "device", cfg.Device)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Identify the device over REST: yields the Beolink JID whose serial
	// component becomes the default device id. Retried forever — the
	// device being off/unreachable at start is normal, not fatal.
	self, err := identify(ctx, cfg.Device, logger)
	if err != nil {
		return err
	}
	serial := serialFromJID(self.Jid)
	deviceID := cfg.DeviceID
	if deviceID == "" {
		deviceID = serial
	}
	logger.Info("device identified",
		"friendly_name", self.FriendlyName, "jid", self.Jid, "device_id", deviceID)

	br, mqttClient, err := connectMQTT(cfg, deviceID, logger)
	if err != nil {
		return err
	}
	br.PublishInfo(bridge.DeviceInfo{
		FriendlyName: self.FriendlyName,
		Jid:          self.Jid,
		Serial:       serial,
		Host:         cfg.Device,
	})

	ws := &mozartws.Client{
		Host:           cfg.Device,
		RemoteControl:  true,
		Logger:         logger,
		OnNotification: br.HandleNotification,
		OnConnect: func() {
			logger.Info("device online", "device", cfg.Device)
			br.PublishAvailability(true)
		},
		OnDisconnect: func(err error) {
			logger.Warn("device offline", "device", cfg.Device, "error", err)
			br.PublishAvailability(false)
		},
	}

	err = ws.Run(ctx)

	// Deliberate on shutdown: with the bridge gone the state is no
	// longer being tracked, so mark the device offline before the
	// graceful MQTT disconnect (which would otherwise suppress the LWT).
	// Synchronous: Disconnect must not race the publish.
	logger.Info("shutting down")
	br.PublishAvailabilitySync(false)
	mqttClient.Disconnect(500)

	if err == context.Canceled {
		return nil
	}
	return err
}

// identify fetches the device's Beolink identity, retrying with backoff
// until it succeeds or ctx is cancelled.
func identify(ctx context.Context, host string, logger *slog.Logger) (*mozartapi.BeolinkSelf, error) {
	restCfg := mozartapi.NewConfiguration()
	restCfg.Servers = mozartapi.ServerConfigurations{{URL: fmt.Sprintf("http://%s", host)}}
	restCfg.HTTPClient = &http.Client{Timeout: 10 * time.Second}
	rest := mozartapi.NewAPIClient(restCfg)

	delay := time.Second
	for {
		self, _, err := rest.BeolinkAPI.GetBeolinkSelf(ctx).Execute()
		if err == nil {
			return self, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		logger.Warn("device not reachable yet, retrying", "device", host, "error", err, "retry_in", delay)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(delay):
		}
		delay = min(delay*2, identifyRetryMax)
	}
}

// serialFromJID extracts the serial number from a Beolink JID like
// "1111.2222222.12345678@products.bang-olufsen.com" → "12345678".
func serialFromJID(jid string) string {
	local, _, _ := strings.Cut(jid, "@")
	parts := strings.Split(local, ".")
	serial := parts[len(parts)-1]
	if serial == "" {
		// A JID we can't parse shouldn't produce broken topic paths;
		// fall back to the whole local part.
		return strings.ToLower(local)
	}
	return strings.ToLower(serial)
}

// connectMQTT establishes the broker session with the last-will registered
// on the bridge's availability topic, and re-announces availability on
// every (re)connect since the broker publishes the will while we're away.
func connectMQTT(cfg *config.Config, deviceID string, logger *slog.Logger) (*bridge.Bridge, mqtt.Client, error) {
	var br *bridge.Bridge

	opts := mqtt.NewClientOptions().
		AddBroker(cfg.BrokerURL).
		SetClientID("beomqtt-" + deviceID).
		SetAutoReconnect(true).
		SetConnectRetry(true).
		SetConnectRetryInterval(5 * time.Second).
		SetMaxReconnectInterval(time.Minute).
		SetOnConnectHandler(func(mqtt.Client) {
			logger.Info("mqtt connected", "broker", cfg.BrokerURL)
			br.PublishAvailability(true)
		}).
		SetConnectionLostHandler(func(_ mqtt.Client, err error) {
			logger.Warn("mqtt connection lost", "error", err)
		})
	if cfg.MQTTUsername != "" {
		opts.SetUsername(cfg.MQTTUsername)
	}
	if cfg.MQTTPassword != "" {
		opts.SetPassword(cfg.MQTTPassword)
	}
	// The will must be set before NewClient: paho copies the options
	// struct there, so later SetWill calls are silently ignored.
	opts.SetWill(cfg.TopicPrefix+"/"+deviceID+"/available", "offline", 1, true)

	client := mqtt.NewClient(opts)
	br = bridge.New(client, cfg.TopicPrefix, deviceID, cfg.PayloadFormat == "flattened", logger)

	token := client.Connect()
	if !token.WaitTimeout(30 * time.Second) {
		return nil, nil, fmt.Errorf("mqtt connect to %s timed out", cfg.BrokerURL)
	}
	if err := token.Error(); err != nil {
		return nil, nil, fmt.Errorf("mqtt connect to %s: %w", cfg.BrokerURL, err)
	}
	return br, client, nil
}
