FROM golang:1.26-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags "-s -w -X main.version=${VERSION}" \
    -o puls-server ./cmd/puls-server
# Baked pre-owned so a fresh named volume mounted at /data (for file-backed
# SQLite via PULS_DB_PATH=/data/...) inherits nobody:nobody ownership on
# first mount, instead of the root:root/755 Docker gives a volume by
# default — which USER 65534:65534 below can't write to.
RUN mkdir -p /data && chown 65534:65534 /data

FROM scratch
COPY --from=builder /app/puls-server /puls-server
COPY --from=builder --chown=65534:65534 /data /data
EXPOSE 8080
# Run as nobody (uid 65534) — no passwd file needed in scratch.
USER 65534:65534
HEALTHCHECK --interval=30s --timeout=5s --retries=3 \
    CMD ["/puls-server", "healthcheck"]
ENTRYPOINT ["/puls-server"]
