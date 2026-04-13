FROM alpine:3.19 AS builder
RUN apk add --no-cache ca-certificates tzdata wget
WORKDIR /build

# Download and install Go for building
RUN wget -q https://go.dev/dl/go1.25.1.linux-amd64.tar.gz && \
    tar -xzf go1.25.1.linux-amd64.tar.gz -C /usr/local && \
    rm go1.25.1.linux-amd64.tar.gz

ENV GOROOT=/usr/local/go
ENV PATH=/usr/local/go/bin:$PATH

COPY go.mod go.sum ./
RUN go mod download

COPY cmd/s3tele ./cmd/s3tele

RUN CGO_ENABLED=0 go build -ldflags="-s -w -extldflags=-static" -o s3tele ./cmd/s3tele

FROM alpine:3.19
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app
COPY --from=builder /build/s3tele ./s3tele
RUN chmod +x s3tele && mkdir -p /app/data

EXPOSE 9000

ENV SERVER_HOST=0.0.0.0
ENV SERVER_PORT=9000
ENV ACCESS_KEY=minioadmin
ENV SECRET_KEY=minioadmin
ENV TELEGRAM_APP_ID=0
ENV TELEGRAM_APP_HASH=
ENV TELEGRAM_GROUP_ID=0
ENV BOT_TOKEN=
ENV BOT_ADMINS=
ENV DATA_DIR=/app/data

CMD ["./s3tele"]