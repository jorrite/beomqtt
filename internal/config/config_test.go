package config

import "testing"

func load(t *testing.T, env map[string]string) (*Config, error) {
	t.Helper()
	base := map[string]string{
		"BEOMQTT_DEVICE":         "192.168.1.10",
		"BEOMQTT_MQTT_URL":       "tcp://broker:1883",
		"BEOMQTT_TOPIC_PREFIX":   "",
		"BEOMQTT_DEVICE_ID":      "",
		"BEOMQTT_LOG_LEVEL":      "",
		"BEOMQTT_PAYLOAD_FORMAT": "",
	}
	for k, v := range env {
		base[k] = v
	}
	for k, v := range base {
		t.Setenv(k, v)
	}
	return Load()
}

func TestDefaults(t *testing.T) {
	cfg, err := load(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.TopicPrefix != "beomqtt" || cfg.BrokerURL != "tcp://broker:1883" {
		t.Errorf("unexpected defaults: %+v", cfg)
	}
	if cfg.PayloadFormat != "flattened" {
		t.Errorf("PayloadFormat default = %q, want \"flattened\"", cfg.PayloadFormat)
	}
}

func TestPayloadFormat(t *testing.T) {
	cfg, err := load(t, map[string]string{"BEOMQTT_PAYLOAD_FORMAT": "json"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PayloadFormat != "json" {
		t.Errorf("PayloadFormat = %q, want \"json\"", cfg.PayloadFormat)
	}

	if _, err := load(t, map[string]string{"BEOMQTT_PAYLOAD_FORMAT": "yaml"}); err == nil {
		t.Error("invalid BEOMQTT_PAYLOAD_FORMAT should be rejected")
	}
}

func TestRequiredVars(t *testing.T) {
	if _, err := load(t, map[string]string{"BEOMQTT_DEVICE": ""}); err == nil {
		t.Error("missing BEOMQTT_DEVICE should fail")
	}
	if _, err := load(t, map[string]string{"BEOMQTT_MQTT_URL": ""}); err == nil {
		t.Error("missing BEOMQTT_MQTT_URL should fail")
	}
}

func TestURLNormalization(t *testing.T) {
	tests := []struct {
		in       string
		broker   string
		user     string
		password string
	}{
		{"mqtt://broker", "tcp://broker:1883", "", ""},
		{"mqtts://broker", "ssl://broker:8883", "", ""},
		{"tcp://u:p@broker:1884", "tcp://broker:1884", "u", "p"},
		{"ws://broker:9001/mqtt", "ws://broker:9001/mqtt", "", ""},
	}
	for _, tt := range tests {
		cfg, err := load(t, map[string]string{"BEOMQTT_MQTT_URL": tt.in})
		if err != nil {
			t.Errorf("%s: %v", tt.in, err)
			continue
		}
		if cfg.BrokerURL != tt.broker || cfg.MQTTUsername != tt.user || cfg.MQTTPassword != tt.password {
			t.Errorf("%s -> broker=%s user=%s pass=%s, want broker=%s user=%s pass=%s",
				tt.in, cfg.BrokerURL, cfg.MQTTUsername, cfg.MQTTPassword, tt.broker, tt.user, tt.password)
		}
	}
}

func TestBadURLs(t *testing.T) {
	for _, in := range []string{"http://broker", "broker:1883", "tcp://"} {
		if _, err := load(t, map[string]string{"BEOMQTT_MQTT_URL": in}); err == nil {
			t.Errorf("%s should be rejected", in)
		}
	}
}
