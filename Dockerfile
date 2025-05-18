FROM golang:1.24-alpine AS builder
RUN apk add --no-cache git ca-certificates
WORKDIR /app
COPY go.mod ./
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /feature-branching

FROM alpine:latest
RUN apk add --no-cache git
COPY --from=builder /feature-branching /usr/local/bin/
COPY entrypoint.sh /entrypoint.sh

RUN chmod +x /entrypoint.sh

ENTRYPOINT ["/entrypoint.sh"]