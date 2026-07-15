FROM golang:1.25-alpine AS builder
WORKDIR /workspace
COPY go.work go.work.sum ./
COPY pkg/ pkg/
COPY core-service/ core-service/
COPY lms-service/ lms-service/
COPY marketing-service/ marketing-service/
COPY video-service/ video-service/
COPY coaching-service/ coaching-service/
COPY mcp-service/ mcp-service/
COPY inbound-smtp-service/ inbound-smtp-service/
RUN cd marketing-service && go build -o /app/marketing-service ./cmd

FROM alpine:3.19
RUN apk --no-cache add ca-certificates
WORKDIR /app
COPY --from=builder /app/marketing-service .
EXPOSE 8082
# OPS-003: image-level liveness so any orchestrator (not just our compose file)
# inherits the health contract.
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 CMD wget -qO- http://localhost:8082/health || exit 1

CMD ["./marketing-service"]
