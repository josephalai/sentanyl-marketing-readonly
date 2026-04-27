FROM golang:1.25-alpine AS builder
WORKDIR /workspace
COPY go.work go.work.sum ./
COPY pkg/ pkg/
COPY core-service/ core-service/
COPY lms-service/ lms-service/
COPY marketing-service/ marketing-service/
COPY video-service/ video-service/
COPY coaching-service/ coaching-service/
RUN cd marketing-service && go build -o /app/marketing-service ./cmd

FROM alpine:3.19
RUN apk --no-cache add ca-certificates
WORKDIR /app
COPY --from=builder /app/marketing-service .
EXPOSE 8082
CMD ["./marketing-service"]
