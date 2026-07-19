# syntax=docker/dockerfile:1

FROM golang:1.26-bookworm AS build
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ ./cmd/
COPY internal/ ./internal/
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w -X main.version=$VERSION" -o /out/beomqtt ./cmd/beomqtt

# distroless/static rather than scratch: brings CA certificates (needed
# for ssl:// broker URLs) and nothing else.
FROM gcr.io/distroless/static-debian12
COPY --from=build /out/beomqtt /beomqtt
ENTRYPOINT ["/beomqtt"]
