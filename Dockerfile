FROM golang:1.25-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o puls-server ./cmd/puls-server
FROM scratch
COPY --from=builder /app/puls-server /puls-server
EXPOSE 8080
ENTRYPOINT ["/puls-server"]
