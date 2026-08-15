# syntax=docker/dockerfile:1

ARG GO_VERSION=1.26.6

# VERSION is stamped into the binary so `docker run yc --version` reports the
# release it was built from. Without it an image built from a tagged tree still
# reports "dev", which makes a bug report from a container unattributable.
# scripts/release-dry-run.sh and .github/workflows/release.yml both pass it.
ARG VERSION=dev

FROM golang:${GO_VERSION}-bookworm AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY cmd ./cmd
COPY internal ./internal

ARG VERSION
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath \
	-ldflags="-s -w -X github.com/worxbend/yc/internal/cli.Version=${VERSION}" \
	-o /out/yc ./cmd/yc

FROM debian:bookworm-slim AS runtime

RUN groupadd --system --gid 10001 yc \
	&& useradd --system --uid 10001 --gid yc --home-dir /home/yc --create-home yc \
	&& mkdir -p /config /cache \
	&& chown -R yc:yc /config /cache /home/yc

# CA certificates are required: every YouTube Data API and Google OAuth call is
# HTTPS, and the scratch-style runtime has no trust store of its own.
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/yc /usr/local/bin/yc

ENV XDG_CONFIG_HOME=/config \
	XDG_CACHE_HOME=/cache \
	TERM=xterm-256color

USER yc:yc
WORKDIR /home/yc

ENTRYPOINT ["yc"]
CMD ["--help"]
