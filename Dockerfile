FROM alpine:3.19
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app
COPY s3tele ./s3tele
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
ENV CHUNK_SIZE=10485760

CMD ["./s3tele"]