// Package config loads beomqtt's deliberately small configuration from
// environment variables. One process bridges exactly one Mozart device to
// one broker; run more instances for more devices.
package config

import (
	"fmt"
	"log/slog"
	"net/url"
	"os"
)

// Config is everything beomqtt can be told. Two variables are required;
// the rest have defaults that should not need touching.
type Config struct {
	// Device is the Mozart device's IP or hostname (BEOMQTT_DEVICE).
	Device string

	// BrokerURL is the sanitized broker address handed to the MQTT
	// client, credentials stripped (e.g. "tcp://broker:1883").
	BrokerURL string
	// MQTTUsername/MQTTPassword come from the userinfo part of
	// BEOMQTT_MQTT_URL, if present.
	MQTTUsername string
	MQTTPassword string

	// TopicPrefix is the first topic segment (BEOMQTT_TOPIC_PREFIX,
	// default "beomqtt").
	TopicPrefix string

	// DeviceID overrides the topic's <device-id> segment
	// (BEOMQTT_DEVICE_ID). Empty means: derive the serial number from
	// the device's Beolink JID.
	DeviceID string

	// LogLevel parsed from BEOMQTT_LOG_LEVEL (default info).
	LogLevel slog.Level

	// PayloadFormat controls how structured (JSON-object) payloads are
	// published: "flattened" (default) — each field as its own retained
	// subtopic, e.g. state/battery/batteryLevel — or "json" — the whole
	// object as one JSON blob at the topic. Never both.
	// (BEOMQTT_PAYLOAD_FORMAT)
	PayloadFormat string
}

// Load reads and validates the environment.
func Load() (*Config, error) {
	cfg := &Config{
		Device:      os.Getenv("BEOMQTT_DEVICE"),
		TopicPrefix: os.Getenv("BEOMQTT_TOPIC_PREFIX"),
		DeviceID:    os.Getenv("BEOMQTT_DEVICE_ID"),
	}
	if cfg.Device == "" {
		return nil, fmt.Errorf("BEOMQTT_DEVICE is required (IP or hostname of the Mozart device)")
	}
	if cfg.TopicPrefix == "" {
		cfg.TopicPrefix = "beomqtt"
	}

	rawURL := os.Getenv("BEOMQTT_MQTT_URL")
	if rawURL == "" {
		return nil, fmt.Errorf("BEOMQTT_MQTT_URL is required (e.g. tcp://user:pass@broker:1883)")
	}
	if err := cfg.parseMQTTURL(rawURL); err != nil {
		return nil, fmt.Errorf("BEOMQTT_MQTT_URL: %w", err)
	}

	level := os.Getenv("BEOMQTT_LOG_LEVEL")
	if level == "" {
		level = "info"
	}
	if err := cfg.LogLevel.UnmarshalText([]byte(level)); err != nil {
		return nil, fmt.Errorf("BEOMQTT_LOG_LEVEL: %w", err)
	}

	cfg.PayloadFormat = os.Getenv("BEOMQTT_PAYLOAD_FORMAT")
	if cfg.PayloadFormat == "" {
		cfg.PayloadFormat = "flattened"
	}
	if cfg.PayloadFormat != "flattened" && cfg.PayloadFormat != "json" {
		return nil, fmt.Errorf(`BEOMQTT_PAYLOAD_FORMAT must be "flattened" or "json", got %q`, cfg.PayloadFormat)
	}

	return cfg, nil
}

func (c *Config) parseMQTTURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}

	// Accept the aliases people actually write and normalize to what
	// paho understands (tcp/ssl/ws/wss).
	switch u.Scheme {
	case "tcp", "ssl", "ws", "wss":
	case "mqtt":
		u.Scheme = "tcp"
	case "mqtts", "tls":
		u.Scheme = "ssl"
	default:
		return fmt.Errorf("unsupported scheme %q (use tcp, ssl, ws, wss, mqtt or mqtts)", u.Scheme)
	}
	if u.Host == "" {
		return fmt.Errorf("missing broker host")
	}
	if u.Port() == "" {
		switch u.Scheme {
		case "tcp":
			u.Host += ":1883"
		case "ssl":
			u.Host += ":8883"
		}
	}

	if u.User != nil {
		c.MQTTUsername = u.User.Username()
		c.MQTTPassword, _ = u.User.Password()
		u.User = nil
	}
	c.BrokerURL = u.String()
	return nil
}
