<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="docs/hero-dark.svg">
    <img src="docs/hero-light.svg" alt="beomqtt — Bang &amp; Olufsen Mozart → MQTT" width="100%">
  </picture>
</p>

<p align="center">
  A tiny bridge from one Bang &amp; Olufsen Mozart-platform device to
  MQTT — state and events in, retained topics out. One static Go binary,
  one process per device.
</p>

> **Unofficial.** Not affiliated with, endorsed by, or supported by
> Bang & Olufsen. This project talks to Mozart devices over the local
> REST/WebSocket API they already expose.

## What this is

Mozart-platform devices (Beolab, Beosound, Beoconnect Core, Beosound
Theatre, …) expose a local REST API (HTTP, port 80) and a WebSocket
notification channel (port 9339) that pushes state changes: playback,
volume, source, now-playing metadata, battery, remote control button
presses. beomqtt subscribes to those notifications and republishes them
as MQTT topics on a broker of your choosing.

It is deliberately generic: nothing here is specific to Home Assistant,
openHAB, Node-RED, Loxone, or any other consumer. If it speaks MQTT, it
can use this bridge. (A sibling project, `beolox`, consumes these topics
to emulate a Loxone Audio Server — beomqtt has no dependency on it, in
either direction.)

What it is **not**:

- It never touches audio. No streams, no resampling, no media proxying —
  JSON in, MQTT out.
- No web UI, no HTTP server, no local state. Everything observable lives
  on the broker; inspect it with MQTT Explorer or `mosquitto_sub`.
- One process bridges exactly **one** device. Run one instance per device
  (containers make this trivial — see below). This is a feature, not a
  limitation: MQTT last-will is per-connection, so with one device per
  process the *broker itself* flips the device's `available` topic to
  `offline` the moment the bridge dies — no stale state, no code.

## Configuration

Environment variables only. Two required, three optional:

| Variable | Required | Default | Purpose |
|---|---|---|---|
| `BEOMQTT_DEVICE` | yes | — | IP or hostname of the Mozart device |
| `BEOMQTT_MQTT_URL` | yes | — | Broker, e.g. `tcp://user:pass@broker:1883`. Schemes: `tcp` (`mqtt`), `ssl` (`mqtts`), `ws`, `wss` |
| `BEOMQTT_TOPIC_PREFIX` | no | `beomqtt` | First topic segment |
| `BEOMQTT_DEVICE_ID` | no | *serial from JID* | Override for the `<device-id>` topic segment |
| `BEOMQTT_LOG_LEVEL` | no | `info` | `debug`, `info`, `warn`, `error` |

The default `<device-id>` is the serial-number component of the device's
Beolink JID (e.g. JID `1111.2222222.12345678@products.bang-olufsen.com`
→ id `12345678`): stable across DHCP lease changes and app renames. Set
`BEOMQTT_DEVICE_ID=kitchen` if you prefer semantic topic paths — that
name is then yours to keep stable. The device's user-visible friendly
name is deliberately *not* used in topic paths (it's editable in the B&O
app, and a rename silently moving every topic would break automations);
it is published on the `info` topic instead.

## Topic reference

All topics live under `<prefix>/<device-id>/` (default `beomqtt/<id>/`).
The complete Mozart notification catalog (37 event types from the
OpenAPI spec) is mapped.

### Core topics (curated payloads)

| Topic | Retained | Payload |
|---|---|---|
| `available` | yes | `online` / `offline` — device reachability; also the bridge's MQTT last-will, so it reads `offline` if the bridge itself dies |
| `info` | yes | JSON: `friendlyName`, `jid`, `serial`, `host` |
| `state/playback` | yes | `idle`, `buffering`, `started`, `paused`, `stopped`, `ended`, `error`, `unknown` |
| `state/volume` | yes | `0`–`100` |
| `state/muted` | yes | `true` / `false` |
| `state/source` | yes | source id, e.g. `spotify`, `lineIn`, `chromeCast` |
| `state/nowplaying` | yes | JSON: `source`, `artist`, `title`, `album`, `genre`, `organization`, `artUrl` |
| `state/battery` | yes | JSON: `batteryLevel`, `isCharging`, `remainingChargingTimeMinutes`, `remainingPlayingTimeMinutes`, `state` (battery devices only) |
| `state/power` | yes | e.g. `on`, `networkStandby` |
| `state/role` | yes | Beolink role, e.g. `standalone` |
| `state/progress` | **no** | JSON: `id`, `progress` (seconds), `totalDuration` — ~1 Hz while playing; not retained because a stale position is worse than none |
| `remote/<key…>` | no | `press` / `release` — Beoremote One buttons, e.g. `remote/control/play` |
| `button/<name>` | no | the device's own physical buttons; payload is Mozart's state string (`shortPress`, `longPress`, `released`, …) |

### Passthrough state topics (retained, raw Mozart JSON)

The payload is the notification's `eventData` verbatim — the shape is
whatever the [Mozart OpenAPI spec] defines for that event.

| Topic | Source notification |
|---|---|
| `state/active_hdmi_input_signal` | ActiveHdmiInputSignal |
| `state/listening_mode` | ActiveListeningMode |
| `state/speaker_group` | ActiveSpeakerGroup |
| `state/speaker_group_config` | SpeakerGroupChanged |
| `state/speaker_link_status` | SpeakerLinkStatusChanged |
| `state/alarm_timer` | AlarmTimer |
| `state/channel_survey_status` | ChannelSurveyStatus |
| `state/classics_adapter_content` | ClassicsAdapterContent |
| `state/curtains` | Curtains |
| `state/hdmi_video_format_signal` | HdmiVideoFormatSignal |
| `state/playback_source` | PlaybackSource |
| `state/powerlink_connection_state` | PowerlinkConnectionState |
| `state/puc_install_remote_id_status` | PucInstallRemoteIdStatus |
| `state/room_compensation_state` | RoomCompensationState |
| `state/software_update_state` | SoftwareUpdateState |
| `state/sound_settings` | SoundSettings |
| `state/stand_connected` | StandConnected |
| `state/stand_position` | StandPosition |
| `state/tv_bno_mode` | TvBnOMode |
| `state/tv_info` | TvInfo |
| `state/wisa_out_state` | WisaOutState |

### Momentary events (not retained, raw Mozart JSON)

| Topic | Source notification |
|---|---|
| `event/alarm_triggered` | AlarmTriggered |
| `event/beolink_experiences_result` | BeolinkExperiencesResult |
| `event/beolink_join_result` | BeolinkJoinResult |
| `event/notification` | Notification (a "re-fetch your config" hint from the device, e.g. `{"value":"configuration"}`) |
| `event/playback_error` | PlaybackError |
| `event/room_compensation_measurement` | RoomCompensationCurrentMeasurementEvent |

State topics are retained so a subscriber connecting later immediately
sees last-known state; on (re)connect the device pushes a full state
snapshot, so retained topics are also re-seeded automatically.

Not yet built: a *derived* Beolink group topic (who leads/follows which
session — needs correlating role, join results and playback metadata;
the raw ingredients are all published above). Commands *to* the device
(MQTT → REST) are a possible v2.

## Running

### Plain binary

```sh
BEOMQTT_DEVICE=192.168.1.23 \
BEOMQTT_MQTT_URL=tcp://broker:1883 \
./beomqtt
```

### Docker

```sh
docker run -d --restart unless-stopped \
  -e BEOMQTT_DEVICE=192.168.1.23 \
  -e BEOMQTT_MQTT_URL=tcp://user:pass@broker:1883 \
  ghcr.io/jorrite/beomqtt   # image name TBD
```

### docker-compose, one service per device

```yaml
services:
  beomqtt-kitchen:
    image: ghcr.io/jorrite/beomqtt
    restart: unless-stopped
    environment:
      BEOMQTT_DEVICE: 192.168.1.23
      BEOMQTT_DEVICE_ID: kitchen
      BEOMQTT_MQTT_URL: tcp://broker:1883
  beomqtt-livingroom:
    image: ghcr.io/jorrite/beomqtt
    restart: unless-stopped
    environment:
      BEOMQTT_DEVICE: 192.168.1.24
      BEOMQTT_DEVICE_ID: livingroom
      BEOMQTT_MQTT_URL: tcp://broker:1883
```

Under Nomad (or any orchestrator), the same applies: one task/alloc per
device, env vars from your template/secret store of choice.

Give devices DHCP reservations or use a resolvable hostname —
`BEOMQTT_DEVICE` with a `.local` mDNS name works where the host OS
resolves mDNS, but not inside minimal containers.

## Behavior notes

- **Resilient by default.** Both the device WebSocket (two endpoints: the
  standard notification stream and the separate Beoremote One stream)
  and the MQTT session reconnect automatically with backoff. Device
  offline ≠ bridge crash: the bridge marks it `offline` and keeps
  retrying forever.
- **Clean shutdown.** SIGINT/SIGTERM publishes `available: offline`,
  closes the WebSockets and the MQTT session gracefully.
- **Startup order.** The bridge first identifies the device over REST
  (retrying until reachable), then connects to the broker with the
  last-will registered, then streams notifications.

## Development

```sh
just run 192.168.1.23 tcp://localhost:1883   # go run against a device
just build                                    # static binary at bin/beomqtt
just docker-build                             # multi-arch image
just check && just test                       # what CI runs
```

Requires `go` and `just` (e.g. via [mise](https://mise.jdx.dev/)), plus
Docker for the image and for regenerating the REST client. For a quick
local broker while testing:

```sh
docker run -d --rm --name mosq -p 1883:1883 eclipse-mosquitto:2 mosquitto -c /mosquitto-no-auth.conf
docker exec mosq mosquitto_sub -t 'beomqtt/#' -v
```

### Architecture, briefly

- `internal/mozartapi` — REST client generated from the official
  [Mozart OpenAPI spec] (vendored in `api/`); regenerate with
  `just generate` (needs Docker). Never edit by hand.
- `internal/mozartws` — hand-written WebSocket client for the
  notification stream (including the separate Beoremote One event
  socket), with jittered-backoff reconnection.
- `internal/bridge` — the notification → topic mapping.

[Mozart OpenAPI spec]: https://github.com/bang-olufsen/mozart-open-api

## License

[MIT](./LICENSE).
