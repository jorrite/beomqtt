default:
    @just --list

# Regenerate internal/mozartapi from the vendored OpenAPI spec (needs Docker).
generate:
    ./scripts/generate-mozart-api.sh

# Static binary, per the deployment goal. VERSION env var (if set) is
# baked into the banner/startup log; release.yml passes the tag.
build:
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=${VERSION:-dev}" -o bin/beomqtt ./cmd/beomqtt

# Run against a device and broker:
#   just run 192.168.1.23 tcp://localhost:1883
run device mqtt_url $BEOMQTT_LOG_LEVEL="debug":
    BEOMQTT_DEVICE={{device}} BEOMQTT_MQTT_URL={{mqtt_url}} go run ./cmd/beomqtt

# Multi-arch Docker image build.
docker-build:
    docker buildx build --platform linux/amd64,linux/arm64 -t beomqtt:local .

# `cmd internal`, not `./...`: keeps gofmt/vet off the generated package's
# style choices where it matters — see check for what's actually enforced.
fmt:
    gofmt -l -w cmd internal/bridge internal/config internal/mozartws
    go vet ./cmd/... ./internal/...

# Read-only static checks — fails rather than fixing, unlike `fmt`.
# gofmt is only enforced on hand-written packages; internal/mozartapi is
# generated output and gets reformatted by the generate script instead.
check:
    test -z "$(gofmt -l cmd internal/bridge internal/config internal/mozartws)"
    go vet ./cmd/... ./internal/...

test:
    go build ./cmd/... ./internal/...
    go test ./cmd/... ./internal/...
