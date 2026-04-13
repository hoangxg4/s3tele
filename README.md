# S3Tele

S3-compatible storage server sử dụng Telegram làm backend storage. Mỗi bucket = một Telegram Group Topic.

## 🚀 Quick Start

### Docker (Khuyến nghị)

```bash
docker run -d \
  -p 9000:9000 \
  -e BOT_TOKEN="your_bot_token" \
  -e TELEGRAM_APP_ID="your_app_id" \
  -e TELEGRAM_APP_HASH="your_app_hash" \
  -e TELEGRAM_GROUP_ID="your_group_id" \
  ghcr.io/username/s3tele:latest
```

### Binary

```bash
# Download từ Releases
./s3tele
```

## ⚙️ Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `SERVER_HOST` | 0.0.0.0 | Server host |
| `SERVER_PORT` | 9000 | Server port |
| `ACCESS_KEY` | minioadmin | Default access key |
| `SECRET_KEY` | minioadmin | Default secret key |
| `BOT_TOKEN` | - | Telegram Bot token (bắt buộc) |
| `BOT_ADMINS` | - | Admin user IDs (comma separated) |
| `DATA_DIR` | ./data | Data directory |
| `TELEGRAM_APP_ID` | - | Telegram App ID (bắt buộc cho MTProto) |
| `TELEGRAM_APP_HASH` | - | Telegram App Hash (bắt buộc cho MTProto) |
| `TELEGRAM_GROUP_ID` | - | Telegram Group ID (topic sẽ là bucket) |

### Cách lấy Telegram Credentials

1. **App ID & App Hash**: 
   - Đăng ký tại https://my.telegram.org/apps
   - Lưu `App ID` và `App Hash`

2. **Group ID**: 
   - Tạo một Supergroup (Forum)
   - Dùng @userinfobot để lấy group ID (thường là số âm như -100xxxxxxxxx)

3. **Bot Token**: 
   - Tạo bot qua @BotFather

## 🤖 Bot Commands

| Command | Description |
|---------|-------------|
| `/start` | Welcome message |
| `/genkey` | Tạo access key mới |
| `/keys` | Xem keys của bạn |
| `/buckets` | List buckets (hiển thị topic ID nếu có) |
| `/createbucket <name>` | Tạo bucket & Telegram Topic |
| `/linktopic <bucket> <topic_id>` | Link bucket với topic có sẵn |
| `/deletebucket <name>` | Xóa bucket & topic |
| `/help` | Help |
| `/help` | Help |

## 📡 S3 API

```bash
export AWS_ACCESS_KEY_ID=s3_12345678_xxxxx
export AWS_SECRET_ACCESS_KEY=your_secret_key

# List buckets
aws --endpoint-url=http://localhost:9000 s3 ls

# Create bucket (tạo Topic trong Group)
aws --endpoint-url=http://localhost:9000 s3 mb s3://mybucket

# Upload file
aws --endpoint-url=http://localhost:9000 s3 cp test.txt s3://mybucket/

# Download file
aws --endpoint-url=http://localhost:9000 s3 cp s3://mybucket/test.txt ./

# Delete file
aws --endpoint-url=http://localhost:9000 s3 rm s3://mybucket/test.txt

# Delete bucket
aws --endpoint-url=http://localhost:9000 s3 rb s3://mybucket
```

## 🔧 Architecture

```
┌─────────────┐     ┌──────────────┐     ┌─────────────────┐
│  S3 Client  │────▶│  S3Tele      │────▶│  Telegram       │
│ (AWS CLI)   │     │  (Bot + API) │     │  Group Topics   │
└─────────────┘     └──────────────┘     └─────────────────┘
                           │
                    ┌──────┴──────┐
                    │  Metadata   │
                    │  (JSON)     │
                    └─────────────┘
```

## 🐳 Docker Build

```bash
# Build binary
export GOROOT=/path/to/go
go build -ldflags="-s -w" -o s3tele ./cmd/s3tele

# Build Docker
docker build -t ghcr.io/username/s3tele:latest .
```

## 📦 Multi-platform Build

```bash
# AMD64
GOOS=linux GOARCH=amd64 go build -o s3tele-linux-amd64 ./cmd/s3tele

# ARM64
GOOS=linux GOARCH=arm64 go build -o s3tele-linux-arm64 ./cmd/s3tele
```

## ⚠️ Lưu ý

- File được upload lên Telegram, không lưu local (trừ metadata)
- Giới hạn file: MTProto không có giới hạn như Bot API (20MB)
- Bot cần là admin của group để tạo/xóa topics

## 📝 License

ISC