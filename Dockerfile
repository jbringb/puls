FROM golang:1.25-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags "-s -w -X main.version=${VERSION}" \
    -o puls-server ./cmd/puls-server

FROM scratch
COPY --from=builder /app/puls-server /puls-server
EXPOSE 8080
# Run as nobody (uid 65534) — no passwd file needed in scratch.
USER 65534:65534
HEALTHCHECK --interval=30s --timeout=5s --retries=3 \
    CMD ["/puls-server", "healthcheck"]
ENTRYPOINT ["/puls-server"]
