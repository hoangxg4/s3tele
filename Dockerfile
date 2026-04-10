ARG BINARY_NAME=s3tele
FROM alpine:3.19 AS builder
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /build
COPY ${BINARY_NAME} s3tele
RUN chmod +x s3tele

FROM alpine:3.19
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app
COPY --from=builder /build/s3tele ./s3tele
RUN chmod +x s3tele
RUN mkdir -p /app/data

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