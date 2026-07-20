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
Theatre, …) expose a local [REST API and WebSocket notification
channel][mozart-api] that pushes state changes: playback, volume,
source, now-playing metadata, battery, remote control button presses.
beomqtt subscribes to those notifications and republishes them as MQTT
topics on a broker of your choosing — no audio touches this process,
just JSON in and MQTT out. It's plain MQTT, so it works with Home
Assistant, openHAB, Node-RED, or anything else that speaks the protocol.

[mozart-api]: https://bang-olufsen.github.io/mozart-open-api/

One process bridges exactly **one** device — run one instance per
speaker (containers make this trivial; see below). MQTT's last-will is
per-connection, so with one device per process the *broker itself* flips
that device's `available` topic to `offline` the instant the bridge
dies, with no code needed to make that true. There's no web UI or local
state either: everything observable lives on the broker, inspectable
with MQTT Explorer or `mosquitto_sub`.

## Configuration

Environment variables only. Two required, four optional:

| Variable | Required | Default | Purpose |
|---|---|---|---|
| `BEOMQTT_DEVICE` | yes | — | IP or hostname of the Mozart device |
| `BEOMQTT_MQTT_URL` | yes | — | Broker, e.g. `tcp://user:pass@broker:1883`. Schemes: `tcp` (`mqtt`), `ssl` (`mqtts`), `ws`, `wss` |
| `BEOMQTT_TOPIC_PREFIX` | no | `beomqtt` | First topic segment |
| `BEOMQTT_DEVICE_ID` | no | *serial from JID* | Override for the `<device-id>` topic segment |
| `BEOMQTT_LOG_LEVEL` | no | `info` | `debug`, `info`, `warn`, `error` |
| `BEOMQTT_PAYLOAD_FORMAT` | no | `flattened` | `flattened` — each field of a structured payload as its own subtopic; `json` — one JSON blob per topic. See below |

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

Every JSON-object payload is published in one of two forms, set by
`BEOMQTT_PAYLOAD_FORMAT` — never both:

- **`flattened`** (default): each field of the decoded object under its
  own subtopic (nested objects become path segments, arrays become
  numeric indices), e.g. `state/battery/batteryLevel`,
  `state/battery/isCharging`, `state/battery/state` instead of a
  `state/battery` blob. Matches the one-value-per-topic convention some
  MQTT ecosystems expect (Homie, Zigbee2MQTT attribute mode, Tasmota).
- **`json`**: the whole object as one blob at the topic itself, e.g.
  `state/battery` → `{"batteryLevel":100,...}`.

Absent/null fields are simply not published, in either form. Switching
`BEOMQTT_PAYLOAD_FORMAT` does not retroactively clear the other format's
retained topics on the broker — old retained blobs or leaves from before
the switch linger until something overwrites or purges them.

### Core topics (curated payloads)

| Topic | Retained | Payload |
|---|---|---|
| `available` | yes | `online` / `offline` — device reachability; also the bridge's MQTT last-will, so it reads `offline` if the bridge itself dies |
| `info` | yes | JSON: `friendlyName`, `jid`, `serial`, `host` |
| `state/playback` | yes | `idle`, `buffering`, `started`, `paused`, `stopped`, `ended`, `error`, `unknown` |
| `state/volume` | yes | `0`–`100`, the convenience scalar most consumers want |
| `state/muted` | yes | `true` / `false`, ditto |
| `state/volume_state` | yes | the full Mozart `VolumeState` object: `level`, `muted`, plus `default` and `maximum` levels that the two scalars above don't carry |
| `state/source` | yes | source id, e.g. `spotify`, `lineIn`, `chromeCast` — the convenience scalar |
| `state/source_state` | yes | the full Mozart `Source` object: `id`, `name` (human-readable, e.g. `"AirPlay"`), `type`, `isEnabled`, `isPlayable`, `isSeekable`, `isMultiroomAvailable` |
| `state/nowplaying` | yes | JSON: the full Mozart `PlaybackContentMetadata` payload as-is (`source`, `artist`, `title`, `album`, `genre`, `organization`, `bitrate`, `samplerate`, `queueId`, `track`, `id`, … — whatever fields the device sends for that source), plus one added field: `artUrl`, the largest artwork URL picked out of the raw `art` array as a convenience (the full `art` array is still there too) |
| `state/battery` | yes | JSON: `batteryLevel`, `isCharging`, `remainingChargingTimeMinutes`, `remainingPlayingTimeMinutes`, `state` (battery devices only) |
| `state/power` | yes | e.g. `on`, `networkStandby` |
| `state/role` | yes | Beolink role, e.g. `standalone` |
| `state/progress` | **no** | JSON: `id`, `progress` (seconds), `totalDuration` — ~1 Hz while playing; not retained because a stale position is worse than none |
| `remote/<key…>` | no | `press` / `release` — Beoremote One buttons, e.g. `remote/control/play` |
| `button/<name>` | no | the device's own physical buttons; payload is Mozart's state string (`shortPress`, `longPress`, `released`, …) |

<details>
<summary><strong>Passthrough topics</strong> — the remaining 27 notification types, raw Mozart JSON (click to expand)</summary>

The payload is the notification's `eventData` verbatim — the shape is
whatever the [Mozart OpenAPI spec] defines for that event.

Retained, under `state/`: `active_hdmi_input_signal`, `listening_mode`,
`speaker_group`, `speaker_group_config`, `speaker_link_status`,
`alarm_timer`, `channel_survey_status`, `classics_adapter_content`,
`curtains`, `hdmi_video_format_signal`, `playback_source`,
`powerlink_connection_state`, `puc_install_remote_id_status`,
`room_compensation_state`, `software_update_state`, `sound_settings`,
`stand_connected`, `stand_position`, `tv_bno_mode`, `tv_info`,
`wisa_out_state` — each name maps to the identically-named
`WebSocketEvent*` notification (e.g. `state/curtains` ← `Curtains`).

Not retained, under `event/`: `alarm_triggered`,
`beolink_experiences_result`, `beolink_join_result`, `notification` (a
"re-fetch your config" hint from the device, e.g.
`{"value":"configuration"}`), `playback_error`,
`room_compensation_measurement`.

</details>

State topics are retained so a subscriber connecting later immediately
sees last-known state; on (re)connect the device pushes a full state
snapshot, so retained topics are also re-seeded automatically.

Not yet built: a *derived* Beolink group topic (who leads/follows which
session — needs correlating role, join results and playback metadata;
the raw ingredients are all published above). Commands *to* the device
(MQTT → REST) are a possible v2.

## Running

```sh
BEOMQTT_DEVICE=192.168.1.23 BEOMQTT_MQTT_URL=tcp://broker:1883 ./beomqtt

# or as a container:
docker run -d --restart unless-stopped \
  -e BEOMQTT_DEVICE=192.168.1.23 \
  -e BEOMQTT_MQTT_URL=tcp://user:pass@broker:1883 \
  ghcr.io/jorrite/beomqtt
```

For multiple devices, run one container per device — with compose:

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

Same idea under Nomad or any orchestrator: one task/alloc per device.
Give devices DHCP reservations or a resolvable hostname — `.local` mDNS
names work if the host OS resolves them, not inside minimal containers.

Both the device WebSocket and the MQTT session reconnect automatically
with backoff, and SIGINT/SIGTERM shuts down cleanly, publishing
`available: offline` first.

## Development

```sh
just run 192.168.1.23 tcp://localhost:1883   # go run against a device
just build                                    # static binary at bin/beomqtt
just check && just test                       # what CI runs
```

Requires `go` and `just`, plus Docker for the image and for regenerating
the REST client (`just generate`, from the vendored [Mozart OpenAPI
spec] — never hand-edit `internal/mozartapi`). Quick local broker:

```sh
docker run -d --rm --name mosq -p 1883:1883 eclipse-mosquitto:2 mosquitto -c /mosquitto-no-auth.conf
docker exec mosq mosquitto_sub -t 'beomqtt/#' -v
```

[Mozart OpenAPI spec]: https://github.com/bang-olufsen/mozart-open-api

## License

[MIT](./LICENSE).
