# Multi-stage so the only build prerequisite is Docker. Blackbox runs amd64,
# so the target is pinned rather than inherited from the build host.
FROM --platform=$BUILDPLATFORM golang:1.26 AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY *.go ./
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags="-s -w" -o /out/backoffice-proxy .

# static-debian12 carries CA certificates (needed to reach the Tailscale
# coordination server and the upstream) and nothing else.
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/backoffice-proxy /backoffice-proxy

# tsnet keeps its node state here. Deliberately ephemeral: no drive is mounted,
# which avoids the drive-mount path in blackbox entirely.
ENV TS_STATE_DIR=/tmp/tsnet

ENTRYPOINT ["/backoffice-proxy"]
