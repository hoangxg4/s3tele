package main

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/message"
	"github.com/gotd/td/telegram/message/html"
	"github.com/gotd/td/telegram/uploader"
	"github.com/gotd/td/tg"
	"gopkg.in/yaml.v3"
)

type Config struct {
	Server    ServerConfig    `yaml:"server"`
	Telegram  TelegramConfig `yaml:"telegram"`
	Storage  StorageConfig `yaml:"storage"`
	Bot      BotConfig      `yaml:"bot"`
}

type ServerConfig struct {
	Host      string `yaml:"host"`
	Port      int    `yaml:"port"`
	AccessKey string `yaml:"accessKey"`
	SecretKey string `yaml:"secretKey"`
}

type TelegramConfig struct {
	AppID   int    `yaml:"appId"`
	AppHash string `yaml:"appHash"`
	GroupID int64  `yaml:"groupId"`
}

type StorageConfig struct {
	ChunkSize int64  `yaml:"chunkSize"`
	DataDir   string `yaml:"dataDir"`
}

type BotConfig struct {
	Token  string   `yaml:"token"`
	Admins []int64  `yaml:"admins"`
}

type UserCredentials struct {
	AccessKey string `json:"accessKey"`
	SecretKey string `json:"secretKey"`
	ChatID   int64   `json:"chatId"`
	GroupID int64   `json:"groupId"`
	CreatedAt int64  `json:"createdAt"`
}

type UserBucket struct {
	Name      string `json:"name"`
	TopicID   int64  `json:"topicId"`
	ChatID    int64  `json:"chatId"`
	CreatedAt int64  `json:"createdAt"`
}

type FileMetadata struct {
	ObjectName          string `json:"objectName"`
	Size              int64  `json:"size"`
	ContentType       string `json:"contentType"`
	ETag              string `json:"etag"`
	MessageID        int64  `json:"messageId"`
	DocumentID       int64  `json:"documentId"`
	DocumentAccessHash int64 `json:"documentAccessHash"`
	CreatedAt        int64  `json:"createdAt"`
}

type BotState struct {
	credentials map[int64]*UserCredentials
	buckets     map[int64]map[string]*UserBucket
	files       map[int64]map[string]map[string]*FileMetadata
	mu          sync.RWMutex
	storagePath string
	dataDir     string
}

func NewBotState(sp, dd string) *BotState {
	os.MkdirAll(dd, 0755)
	s := &BotState{
		credentials: make(map[int64]*UserCredentials),
		buckets:     make(map[int64]map[string]*UserBucket),
		files:       make(map[int64]map[string]map[string]*FileMetadata),
		storagePath: sp,
		dataDir:     dd,
	}
	if d, err := os.ReadFile(sp); err == nil {
		var sd struct {
			Credentials map[int64]*UserCredentials      `json:"credentials"`
			Buckets     map[int64]map[string]*UserBucket  `json:"buckets"`
			Files       map[int64]map[string]map[string]*FileMetadata `json:"files"`
		}
		if json.Unmarshal(d, &sd) == nil {
			s.credentials = sd.Credentials
			s.buckets = sd.Buckets
			s.files = sd.Files
		}
	}
	return s
}

func (s *BotState) save() {
	d, _ := json.MarshalIndent(map[string]interface{}{"credentials": s.credentials, "buckets": s.buckets, "files": s.files}, "", "  ")
	os.WriteFile(s.storagePath, d, 0644)
}

func (s *BotState) GetCredentials(chatID int64) *UserCredentials { s.mu.RLock(); defer s.mu.RUnlock(); return s.credentials[chatID] }
func (s *BotState) SetCredentials(chatID int64, c *UserCredentials) { s.mu.Lock(); s.credentials[chatID] = c; s.mu.Unlock(); s.save() }
func (s *BotState) GetUserGroupID(chatID int64) int64 { s.mu.RLock(); defer s.mu.RUnlock(); if c, ok := s.credentials[chatID]; ok { return c.GroupID }; return 0 }
func (s *BotState) GetBuckets(chatID int64) map[string]*UserBucket { s.mu.RLock(); defer s.mu.RUnlock(); if _, ok := s.buckets[chatID]; !ok { s.buckets[chatID] = make(map[string]*UserBucket) }; return s.buckets[chatID] }
func (s *BotState) GetBucket(chatID int64, name string) *UserBucket { s.mu.RLock(); defer s.mu.RUnlock(); if b, ok := s.buckets[chatID]; ok { return b[name] }; return nil }
func (s *BotState) SetBucket(chatID int64, b *UserBucket, topicID int64) { s.mu.Lock(); if _, ok := s.buckets[chatID]; !ok { s.buckets[chatID] = make(map[string]*UserBucket) }; b.TopicID = topicID; s.buckets[chatID][b.Name] = b; s.mu.Unlock(); s.save() }
func (s *BotState) DeleteBucket(chatID int64, name string) { s.mu.Lock(); if b, ok := s.buckets[chatID]; ok { delete(b, name) }; if f, ok := s.files[chatID]; ok { delete(f, name) }; s.mu.Unlock(); s.save() }
func (s *BotState) SetFile(chatID int64, bucket string, m *FileMetadata) { s.mu.Lock(); if _, ok := s.files[chatID]; !ok { s.files[chatID] = make(map[string]map[string]*FileMetadata) }; if _, ok := s.files[chatID][bucket]; !ok { s.files[chatID][bucket] = make(map[string]*FileMetadata) }; s.files[chatID][bucket][m.ObjectName] = m; s.mu.Unlock(); s.save() }
func (s *BotState) GetFile(chatID int64, bucket, obj string) *FileMetadata { s.mu.RLock(); defer s.mu.RUnlock(); if f, ok := s.files[chatID]; ok { if b, ok := f[bucket]; ok { return b[obj] } }; return nil }
func (s *BotState) DeleteFile(chatID int64, bucket, obj string) { s.mu.Lock(); if f, ok := s.files[chatID]; ok { if b, ok := f[bucket]; ok { delete(b, obj) } }; s.mu.Unlock(); s.save() }

type Storage struct {
	config    StorageConfig
	botState  *BotState
	tgClient  *telegram.Client
	tgReady   chan bool
	tgReadyOK bool
	groupID   int64
}

func (s *Storage) waitForTelegram(timeout time.Duration) error {
	log.Printf("[DEBUG] waitForTelegram: tgClient=%v, tgReady=%v, tgReadyOK=%v", s.tgClient != nil, s.tgReady != nil, s.tgReadyOK)
	if s.tgClient == nil {
		return fmt.Errorf("Telegram not configured")
	}
	if s.tgReadyOK {
		log.Printf("[DEBUG] Telegram already connected")
		return nil
	}
	if s.tgReady == nil {
		return fmt.Errorf("Telegram not initialized")
	}
	log.Printf("[DEBUG] Waiting for Telegram to be ready...")
	select {
	case ok, okChan := <-s.tgReady:
		if !okChan {
			log.Printf("[DEBUG] Telegram channel closed")
			return fmt.Errorf("Telegram channel closed")
		}
		log.Printf("[DEBUG] Telegram ready: ok=%v", ok)
		if !ok {
			return fmt.Errorf("Telegram connection failed")
		}
		s.tgReadyOK = true
		return nil
	case <-time.After(timeout):
		log.Printf("[DEBUG] Telegram connection timeout")
		return fmt.Errorf("Telegram connection timeout")
	}
}

func (s *Storage) UploadObject(ctx context.Context, chatID int64, bucket, obj string, data []byte) (string, int64, error) {
	hash := md5.Sum(data)
	etag := "\"" + hex.EncodeToString(hash[:]) + "\""

	if s.tgClient == nil {
		return "", 0, fmt.Errorf("Telegram client not initialized")
	}

	if err := s.waitForTelegram(10 * time.Second); err != nil {
		return "", 0, err
	}

	api := tg.NewClient(s.tgClient)
	u := uploader.NewUploader(api)
	sender := message.NewSender(api).WithUploader(u)

	bucketInfo := s.botState.GetBucket(chatID, bucket)
	if bucketInfo == nil {
		return "", 0, fmt.Errorf("bucket not found")
	}

	chunkSize := int(s.config.ChunkSize)
	if chunkSize == 0 {
		chunkSize = 10 * 1024 * 1024 // Default 10MB chunks
	}

	var finalMsgID, finalDocID, finalDocAccessHash int64

	// Chunking for large files
	if len(data) > chunkSize {
		log.Printf("Uploading large file %d bytes in %d chunks", len(data), (len(data)+chunkSize-1)/chunkSize)
		partID := 0
		for offset := 0; offset < len(data); offset += chunkSize {
			end := offset + chunkSize
			if end > len(data) {
				end = len(data)
			}
			chunk := data[offset:end]

			tmpFile := fmt.Sprintf("/tmp/s3tele_part_%d_%s", partID, obj)
			if err := os.WriteFile(tmpFile, chunk, 0644); err != nil {
				return "", 0, err
			}

			upload, err := u.FromPath(ctx, tmpFile)
			if err != nil {
				os.Remove(tmpFile)
				return "", 0, fmt.Errorf("chunk upload failed: %w", err)
			}

			doc := message.UploadedDocument(upload, html.String(nil, fmt.Sprintf("%s.part%d", obj, partID))).
				MIME(getContentType(obj)).
				Filename(fmt.Sprintf("%s.part%d", obj, partID))

			peer := &tg.InputPeerChat{ChatID: s.groupID}
			var result tg.UpdatesClass
			var sendErr error

			if bucketInfo.TopicID != 0 {
				result, sendErr = sender.To(peer).Reply(int(bucketInfo.TopicID)).Media(ctx, doc)
			} else {
				result, sendErr = sender.To(peer).Media(ctx, doc)
			}

			os.Remove(tmpFile)

			if sendErr != nil {
				return "", 0, fmt.Errorf("send chunk failed: %w", sendErr)
			}

			msgID, docID, docAccessHash := extractMessageInfo(result)
			if partID == 0 {
				finalMsgID = int64(msgID)
				finalDocID = docID
				finalDocAccessHash = docAccessHash
			}
			partID++
		}
	} else {
		tmpFile := "/tmp/s3tele_upload_" + obj
		if err := os.WriteFile(tmpFile, data, 0644); err != nil {
			return "", 0, err
		}
		defer os.Remove(tmpFile)

		upload, err := u.FromPath(ctx, tmpFile)
		if err != nil {
			return "", 0, fmt.Errorf("upload failed: %w", err)
		}

		doc := message.UploadedDocument(upload, html.String(nil, obj)).
			MIME(getContentType(obj)).
			Filename(obj)

		peer := &tg.InputPeerChat{ChatID: s.groupID}

		var result tg.UpdatesClass
		var sendErr error
		if bucketInfo.TopicID != 0 {
			result, sendErr = sender.To(peer).Reply(int(bucketInfo.TopicID)).Media(ctx, doc)
		} else {
			result, sendErr = sender.To(peer).Media(ctx, doc)
		}

		if sendErr != nil {
			return "", 0, fmt.Errorf("send failed: %w", sendErr)
		}

		msgID, docID, docAccessHash := extractMessageInfo(result)
		if msgID == 0 {
			return "", 0, fmt.Errorf("could not extract message ID from result")
		}

		finalMsgID = int64(msgID)
		finalDocID = docID
		finalDocAccessHash = docAccessHash
	}

	m := &FileMetadata{
		ObjectName:           obj,
		Size:               int64(len(data)),
		ContentType:        getContentType(obj),
		ETag:               etag,
		MessageID:         finalMsgID,
		DocumentID:        finalDocID,
		DocumentAccessHash: finalDocAccessHash,
		CreatedAt:        time.Now().UnixMilli(),
	}
	s.botState.SetFile(chatID, bucket, m)
	return m.ETag, m.Size, nil
}

func (s *Storage) GetObject(ctx context.Context, chatID int64, bucket, obj string) ([]byte, error) {
	m := s.botState.GetFile(chatID, bucket, obj)
	if m == nil {
		return nil, fmt.Errorf("not found")
	}

	if s.tgClient == nil || m.DocumentID == 0 {
		return nil, fmt.Errorf("file not available")
	}

	if err := s.waitForTelegram(10 * time.Second); err != nil {
		return nil, err
	}

	api := tg.NewClient(s.tgClient)

	loc := &tg.InputDocumentFileLocation{
		ID:         m.DocumentID,
		AccessHash: m.DocumentAccessHash,
	}

	fileResp, err := api.UploadGetFile(ctx, &tg.UploadGetFileRequest{
		Location: loc,
		Offset:    0,
		Limit:    10 * 1024 * 1024,
	})
	if err != nil {
		return nil, err
	}

	if f, ok := fileResp.(*tg.UploadFile); ok && len(f.Bytes) > 0 {
		return f.Bytes, nil
	}

	return nil, fmt.Errorf("could not download file")
}

func (s *Storage) DeleteObject(ctx context.Context, chatID int64, bucket, obj string) error {
	m := s.botState.GetFile(chatID, bucket, obj)
	if m == nil {
		return fmt.Errorf("not found")
	}

	if s.tgClient != nil && m.DocumentID != 0 {
		if err := s.waitForTelegram(10 * time.Second); err != nil {
			log.Printf("Telegram not ready for delete: %v", err)
		} else {
			api := tg.NewClient(s.tgClient)
			_, err := api.InvokeJSON(ctx, `{"@type":"messages.deleteMessages","id":[`+fmt.Sprintf("%d", m.MessageID)+`],"revoke":true}`, true)
			if err != nil {
				log.Printf("Failed to delete Telegram message: %v", err)
			}
		}
	}

	s.botState.DeleteFile(chatID, bucket, obj)
	return nil
}

func extractMessageInfo(result tg.UpdatesClass) (msgID int, docID int64, docAccessHash int64) {
	switch u := result.(type) {
	case *tg.Updates:
		for _, update := range u.Updates {
			if m, ok := update.(*tg.UpdateNewMessage); ok {
				if msg, ok := m.Message.(*tg.Message); ok {
					msgID = msg.ID
					if media, ok := msg.Media.(*tg.MessageMediaDocument); ok {
						if doc, ok := media.Document.AsNotEmpty(); ok {
							docID = doc.ID
							docAccessHash = doc.AccessHash
						}
					}
					return
				}
			}
		}
	}
	return 0, 0, 0
}

func getContentType(f string) string {
	for i := len(f) - 1; i >= 0; i-- {
		if f[i] == '.' {
			mime := map[string]string{"txt": "text/plain", "html": "text/html", "css": "text/css", "js": "application/javascript", "json": "application/json", "xml": "application/xml", "pdf": "application/pdf", "zip": "application/zip", "jpg": "image/jpeg", "png": "image/png", "gif": "image/gif", "mp4": "video/mp4", "mp3": "audio/mpeg"}
			if v, ok := mime[f[i+1:]]; ok { return v }
			break
		}
	}
	return "application/octet-stream"
}

type S3Server struct {
	storage  *Storage
	config   ServerConfig
	botState *BotState
	botToken string
	mux      *http.ServeMux
	groupID  int64
}

func NewS3Server(s *Storage, cfg ServerConfig, bs *BotState, bt string, admins []int64, groupID int64) *S3Server {
	m := make(map[int64]bool)
	for _, a := range admins { m[a] = true }
	return &S3Server{storage: s, config: cfg, botState: bs, botToken: bt, mux: http.NewServeMux(), groupID: groupID}
}

func (s *S3Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	auth := r.Header.Get("Authorization")
	if auth == "" { s.err(w, 401, "No auth"); return }
	var ak, sk string
	if strings.HasPrefix(auth, "Basic ") {
		if d, err := hex.DecodeString(strings.TrimPrefix(auth, "Basic ")); err == nil {
			if p := strings.SplitN(string(d), ":", 2); len(p) == 2 { ak, sk = p[0], p[1] }
		}
	}
	if ak == "" || sk == "" { s.err(w, 401, "Invalid"); return }
	var cred *UserCredentials
	for _, c := range s.botState.credentials {
		if c.AccessKey == ak && c.SecretKey == sk { cred = c; break }
	}
	if cred == nil { s.err(w, 403, "Invalid"); return }
	p := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/"), "/", 2)
	bucket, obj := "", ""
	if len(p) >= 1 { bucket = p[0] }
	if len(p) >= 2 { obj = p[1] }
	switch r.Method {
	case "GET":
		if bucket == "" { s.listBuckets(w, cred) } else if obj == "" { s.listObjects(w, cred, bucket) } else { s.getObject(w, r, cred, bucket, obj) }
	case "PUT":
		if obj == "" { s.createBucket(w, cred, bucket) } else { s.putObject(w, r, cred, bucket, obj) }
	case "DELETE":
		if obj == "" { s.deleteBucket(w, cred, bucket) } else { s.deleteObject(w, cred, bucket, obj) }
	case "HEAD": s.headObject(w, cred, bucket, obj)
	default: w.Header().Set("Allow", "GET,PUT,HEAD,DELETE"); s.err(w, 405, "Method not allowed")
	}
}

func (s *S3Server) listBuckets(w http.ResponseWriter, cred *UserCredentials) {
	b := s.botState.GetBuckets(cred.ChatID)
	var items []BucketInfo
	for n, b := range b { items = append(items, BucketInfo{Name: n, Created: time.UnixMilli(b.CreatedAt)}) }
	s.xml(w, ListBucketsResp{XMLNS: "http://s3.amazonaws.com/doc/2006-03-01/", Buckets: BucketList{Items: items}, Owner: Owner{ID: cred.AccessKey}})
}

func (s *S3Server) listObjects(w http.ResponseWriter, cred *UserCredentials, bucket string) {
	if _, ok := s.botState.GetBuckets(cred.ChatID)[bucket]; !ok { s.err(w, 404, "Bucket not found"); return }
	var c []Content
	s.botState.mu.RLock()
	if f, ok := s.botState.files[cred.ChatID]; ok {
		if bf, ok := f[bucket]; ok {
			for _, m := range bf { c = append(c, Content{Key: m.ObjectName, Size: m.Size, ETag: m.ETag, LastModified: time.UnixMilli(m.CreatedAt)}) }
		}
	}
	s.botState.mu.RUnlock()
	s.xml(w, ListObjectsResp{XMLNS: "http://s3.amazonaws.com/doc/2006-03-01/", Name: bucket, Contents: c})
}

func (s *S3Server) createBucket(w http.ResponseWriter, cred *UserCredentials, bucket string) {
	if strings.Contains(bucket, "..") || strings.Contains(bucket, "/") { s.err(w, 400, "Invalid bucket name"); return }
	if _, e := s.botState.GetBuckets(cred.ChatID)[bucket]; e { s.err(w, 409, "Bucket exists"); return }
	s.botState.SetBucket(cred.ChatID, &UserBucket{Name: bucket, ChatID: cred.ChatID, TopicID: 0, CreatedAt: time.Now().UnixMilli()}, 0)
	w.WriteHeader(200)
}

func (s *S3Server) putObject(w http.ResponseWriter, r *http.Request, cred *UserCredentials, bucket, obj string) {
	data, _ := io.ReadAll(r.Body)
	etag, size, err := s.storage.UploadObject(context.Background(), cred.ChatID, bucket, obj, data)
	if err != nil { s.err(w, 500, err.Error()); return }
	w.Header().Set("ETag", etag)
	w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	w.WriteHeader(200)
}

func (s *S3Server) getObject(w http.ResponseWriter, r *http.Request, cred *UserCredentials, bucket, obj string) {
	data, err := s.storage.GetObject(context.Background(), cred.ChatID, bucket, obj)
	if err != nil { s.err(w, 404, err.Error()); return }
	m := s.botState.GetFile(cred.ChatID, bucket, obj)
	w.Header().Set("ETag", m.ETag)
	w.Header().Set("Content-Length", strconv.FormatInt(m.Size, 10))
	w.Header().Set("Content-Type", m.ContentType)
	w.WriteHeader(200)
	w.Write(data)
}

func (s *S3Server) deleteBucket(w http.ResponseWriter, cred *UserCredentials, bucket string) { s.botState.DeleteBucket(cred.ChatID, bucket); w.WriteHeader(204) }
func (s *S3Server) deleteObject(w http.ResponseWriter, cred *UserCredentials, bucket, obj string) { s.storage.DeleteObject(context.Background(), cred.ChatID, bucket, obj); w.WriteHeader(204) }

func (s *S3Server) headObject(w http.ResponseWriter, cred *UserCredentials, bucket, obj string) {
	m := s.botState.GetFile(cred.ChatID, bucket, obj)
	if m == nil { s.err(w, 404, "Not found"); return }
	w.Header().Set("ETag", m.ETag)
	w.Header().Set("Content-Length", strconv.FormatInt(m.Size, 10))
	w.Header().Set("Content-Type", m.ContentType)
	w.WriteHeader(200)
}

func (s *S3Server) err(w http.ResponseWriter, code int, msg string) {
	w.WriteHeader(code)
	w.Header().Set("Content-Type", "application/xml")
	xml.NewEncoder(w).Encode(ErrorResp{Code: http.StatusText(code), Message: msg, RequestID: fmt.Sprintf("%x", md5.Sum([]byte(time.Now().String())))})
}

func (s *S3Server) xml(w http.ResponseWriter, v interface{}) { w.Header().Set("Content-Type", "application/xml"); xml.NewEncoder(w).Encode(v) }
func (s *S3Server) Start(addr string) error { s.mux.HandleFunc("/", s.ServeHTTP); return http.ListenAndServe(addr, s.mux) }

type ListBucketsResp struct {
	XMLName xml.Name   `xml:"ListAllMyBucketsResult"`
	XMLNS   string     `xml:"xmlns,attr"`
	Buckets BucketList `xml:"Buckets"`
	Owner   Owner      `xml:"Owner"`
}

type BucketList struct {
	Items []BucketInfo `xml:"Bucket"`
}

type BucketInfo struct {
	Name    string    `xml:"Name"`
	Created time.Time `xml:"CreationDate"`
}

type Owner struct {
	ID string `xml:"ID"`
}

type ListObjectsResp struct {
	XMLName  xml.Name   `xml:"ListBucketResult"`
	XMLNS    string     `xml:"xmlns,attr"`
	Name     string     `xml:"Name"`
	Contents []Content  `xml:"Contents"`
}

type Content struct {
	Key          string    `xml:"Key"`
	Size         int64     `xml:"Size"`
	ETag         string    `xml:"ETag"`
	LastModified time.Time `xml:"LastModified"`
}

type ErrorResp struct {
	Code      string `xml:"Code"`
	Message   string `xml:"Message"`
	RequestID string `xml:"RequestId"`
}

type BotUpdate struct {
	UpdateID int64      `json:"update_id"`
	Message  BotMessage `json:"message"`
}

type BotMessage struct {
	MessageID int64  `json:"message_id"`
	From      BotUser `json:"from"`
	Chat      BotChat `json:"chat"`
	Text      string `json:"text"`
}

type BotUser struct {
	ID int64 `json:"id"`
}

type BotChat struct {
	ID int64 `json:"id"`
}

func (s *S3Server) startBot(ctx context.Context) {
	if s.botToken == "" { return }
	var offset int64
	t := time.NewTicker(1 * time.Second)
	defer t.Stop()
	fmt.Println("🤖 Bot polling...")
	for {
		select {
		case <-ctx.Done(): return
		case <-t.C:
			url := fmt.Sprintf("https://api.telegram.org/bot%s/getUpdates?timeout=60&offset=%d", s.botToken, offset)
			var r struct {
				OK bool `json:"ok"`
				Result []BotUpdate `json:"result"`
			}
			if resp, err := http.Get(url); err == nil {
				if json.NewDecoder(resp.Body).Decode(&r) == nil && r.OK {
					for _, u := range r.Result {
						offset = u.UpdateID + 1
						s.handleBot(u.Message)
					}
				}
				resp.Body.Close()
			}
		}
	}
}

func (s *S3Server) handleBot(msg BotMessage) {
	cid := msg.Chat.ID
	txt := msg.Text
	if txt == "" || !strings.HasPrefix(txt, "/") { return }
	p := strings.Fields(txt)
	cmd := p[0]
	var rsp string
	switch cmd {
	case "/start":
		rsp = "S3Tele Bot\n/keys /genkey /buckets /createbucket /help"
	case "/help":
		rsp = "/keys - show keys\n/genkey - new keys\n/buckets - list\n/createbucket <name> - create bucket & topic\n/deletebucket <name>\n/linktopic <bucket> <topic_id>"
	case "/linktopic":
		if len(p) < 3 { rsp = "Usage: /linktopic <bucket> <topic_id>" } else {
			name := p[1]
			topicID, _ := strconv.ParseInt(p[2], 10, 64)
			if topicID == 0 {
				rsp = "Invalid topic ID"
			} else {
				bucket := s.botState.GetBucket(cid, name)
				if bucket == nil {
					rsp = "Bucket not found"
				} else {
					bucket.TopicID = topicID
					s.botState.SetBucket(cid, bucket, topicID)
					rsp = fmt.Sprintf("✅ Linked `%s` to topic %d", name, topicID)
				}
			}
		}
	case "/keys":
		if c := s.botState.GetCredentials(cid); c != nil {
			rsp = fmt.Sprintf("AK: `%s`\nSK: `%s`", c.AccessKey, c.SecretKey)
		} else {
			rsp = "Run /genkey"
		}
	case "/genkey":
		ak, sk := fmt.Sprintf("s3_%d_%s", cid, rs(8)), rs(32)
		s.botState.SetCredentials(cid, &UserCredentials{AccessKey: ak, SecretKey: sk, ChatID: cid, GroupID: s.botState.GetUserGroupID(cid), CreatedAt: time.Now().UnixMilli()})
		rsp = fmt.Sprintf("✅ AK: `%s`\nSK: `%s`", ak, sk)
	case "/buckets":
		b := s.botState.GetBuckets(cid)
		if len(b) == 0 {
			rsp = "No buckets"
		} else {
			var ns []string
			for n, b := range b {
				topicInfo := ""
				if b.TopicID != 0 {
					topicInfo = fmt.Sprintf(" (topic: %d)", b.TopicID)
				}
				ns = append(ns, n+topicInfo)
			}
			rsp = strings.Join(ns, "\n")
		}
	case "/createbucket":
		if len(p) < 2 {
			rsp = "Usage: /createbucket <name>"
		} else {
			name := p[1]
			if _, e := s.botState.GetBuckets(cid)[name]; e {
				rsp = "Exists"
			} else {
				var topicID int64
				if s.storage.tgClient != nil && s.groupID != 0 {
					log.Printf("[DEBUG] /createbucket: waiting for Telegram")
					if err := s.storage.waitForTelegram(10 * time.Second); err != nil {
						rsp = fmt.Sprintf("Telegram error: %v", err)
					} else {
						log.Printf("[DEBUG] /createbucket: creating topic in group %d", s.groupID)
						api := tg.NewClient(s.storage.tgClient)
						peer := &tg.InputPeerChat{ChatID: s.groupID}
						log.Printf("[DEBUG] /createbucket: calling MessagesCreateForumTopic")
						resp, err := api.MessagesCreateForumTopic(context.Background(), &tg.MessagesCreateForumTopicRequest{
							Peer:     peer,
							Title:    name,
							RandomID: time.Now().UnixNano(),
						})
						log.Printf("[DEBUG] /createbucket: resp=%T, err=%v", resp, err)
						if err != nil {
							rsp = fmt.Sprintf("Error: %v", err)
						} else {
							result, err := api.MessagesGetForumTopics(context.Background(), &tg.MessagesGetForumTopicsRequest{
								Peer:  peer,
								Q:     name,
								Limit: 1,
							})
							if err == nil && len(result.Topics) > 0 {
								if t, ok := result.Topics[0].(*tg.ForumTopic); ok {
									topicID = int64(t.ID)
								}
							}
							if topicID != 0 {
								s.botState.SetBucket(cid, &UserBucket{Name: name, ChatID: cid, TopicID: topicID, CreatedAt: time.Now().UnixMilli()}, topicID)
								rsp = fmt.Sprintf("✅ `%s` created (Topic: %d)", name, topicID)
							} else {
								rsp = fmt.Sprintf("✅ Topic `%s` created. Use /linktopic to link to bucket.", name)
							}
						}
					}
				} else {
					s.botState.SetBucket(cid, &UserBucket{Name: name, ChatID: cid, TopicID: 0, CreatedAt: time.Now().UnixMilli()}, 0)
					rsp = fmt.Sprintf("✅ `%s` created locally", name)
				}
			}
		}
	case "/deletebucket":
		if len(p) < 2 {
			rsp = "Usage: /deletebucket <name>"
		} else {
			name := p[1]
			bucket := s.botState.GetBucket(cid, name)
			if bucket != nil && bucket.TopicID != 0 && s.storage.tgClient != nil && s.groupID != 0 {
				api := tg.NewClient(s.storage.tgClient)
				peer := &tg.InputPeerChat{ChatID: s.groupID}
				api.MessagesDeleteTopicHistory(context.Background(), &tg.MessagesDeleteTopicHistoryRequest{
					Peer:     peer,
					TopMsgID: int(bucket.TopicID),
				})
			}
			s.botState.DeleteBucket(cid, name)
			rsp = fmt.Sprintf("✅ `%s` deleted", name)
		}
	}
	if rsp != "" { s.sendMsg(cid, rsp) }
}

func (s *S3Server) sendMsg(cid int64, txt string) {
	http.Post(fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", s.botToken), "application/json", strings.NewReader(fmt.Sprintf(`{"chat_id":%d,"text":"%s"}`, cid, txt)))
}

func rs(n int) string {
	const c = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b := make([]byte, n)
	for i := range b { b[i] = c[int(time.Now().UnixNano()+int64(i)*31)%len(c)] }
	return string(b)
}

func getenv(k, def string) string { if v := os.Getenv(k); v != "" { return v }; return def }
func geteni(k string, def int) int { if v := os.Getenv(k); v != "" { i, _ := strconv.Atoi(v); return i }; return def }
func geten64(k string, def int64) int64 { if v := os.Getenv(k); v != "" { i, _ := strconv.ParseInt(v, 10, 64); return i }; return def }
func getenis(k string) []int64 { if v := os.Getenv(k); v != "" { var r []int64; for _, s := range strings.Split(v, ",") { if i, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64); err == nil { r = append(r, i) } }; return r }; return nil }

func loadCfg(path string) (*Config, error) {
	d, e := os.ReadFile(path)
	if e != nil { return nil, e }
	var c Config
	if e := yaml.Unmarshal(d, &c); e != nil { return nil, e }
	return &c, nil
}

func main() {
	cp := flag.String("config", "", "Config file")
	flag.Parse()

	var cfg *Config
	if *cp != "" {
		c, err := loadCfg(*cp)
		if err != nil { log.Fatalf("Load: %v", err) }
		cfg = c
	} else {
		cfg = &Config{
			Server:   ServerConfig{Host: getenv("SERVER_HOST", "0.0.0.0"), Port: geteni("SERVER_PORT", 9000), AccessKey: getenv("ACCESS_KEY", "minioadmin"), SecretKey: getenv("SECRET_KEY", "minioadmin")},
			Telegram: TelegramConfig{AppID: geteni("TELEGRAM_APP_ID", 0), AppHash: getenv("TELEGRAM_APP_HASH", ""), GroupID: geten64("TELEGRAM_GROUP_ID", 0)},
			Storage:  StorageConfig{DataDir: getenv("DATA_DIR", "./data")},
			Bot:      BotConfig{Token: getenv("BOT_TOKEN", ""), Admins: getenis("BOT_ADMINS")},
		}
	}

	for _, e := range []string{"SERVER_HOST", "SERVER_PORT", "ACCESS_KEY", "SECRET_KEY", "BOT_TOKEN", "BOT_ADMINS", "DATA_DIR", "TELEGRAM_APP_ID", "TELEGRAM_APP_HASH", "TELEGRAM_GROUP_ID"} {
		if v := os.Getenv(e); v != "" {
			switch e {
			case "SERVER_HOST": cfg.Server.Host = v
			case "SERVER_PORT": cfg.Server.Port, _ = strconv.Atoi(v)
			case "ACCESS_KEY": cfg.Server.AccessKey = v
			case "SECRET_KEY": cfg.Server.SecretKey = v
			case "BOT_TOKEN": cfg.Bot.Token = v
			case "BOT_ADMINS": cfg.Bot.Admins = getenis("BOT_ADMINS")
			case "DATA_DIR": cfg.Storage.DataDir = v
			case "TELEGRAM_APP_ID": cfg.Telegram.AppID, _ = strconv.Atoi(v)
			case "TELEGRAM_APP_HASH": cfg.Telegram.AppHash = v
			case "TELEGRAM_GROUP_ID": cfg.Telegram.GroupID, _ = strconv.ParseInt(v, 10, 64)
			}
		}
	}

	fmt.Println("🔄 S3Tele starting...")

	// Initialize Telegram client
	var tgClient *telegram.Client
	var tgReady chan bool
	if cfg.Telegram.AppID > 0 && cfg.Telegram.AppHash != "" && cfg.Bot.Token != "" {
		log.Printf("[DEBUG] Initializing Telegram client with AppID=%d, GroupID=%d", cfg.Telegram.AppID, cfg.Telegram.GroupID)
		tgReady = make(chan bool, 1)
		tgClient = telegram.NewClient(cfg.Telegram.AppID, cfg.Telegram.AppHash, telegram.Options{
			NoUpdates: true,
		})
		go func() {
			if err := tgClient.Run(context.Background(), func(ctx context.Context) error {
				_, err := tgClient.Auth().Bot(ctx, cfg.Bot.Token)
				if err != nil {
					log.Printf("Telegram auth error: %v", err)
					return err
				}
				log.Println("✅ Telegram connected")
				tgReady <- true
				return nil
			}); err != nil {
				log.Printf("Telegram client error: %v", err)
				tgReady <- false
			}
			close(tgReady)
		}()
	} else {
		log.Printf("[DEBUG] Telegram not configured: AppID=%d, AppHash=%s, Token=%s", cfg.Telegram.AppID, cfg.Telegram.AppHash, cfg.Bot.Token)
	}

	dd := cfg.Storage.DataDir
	if dd == "" { dd = "./data" }
	os.MkdirAll(dd, 0755)
	botState := NewBotState(filepath.Join(dd, "bot_state.json"), dd)

	storage := &Storage{config: cfg.Storage, botState: botState, tgClient: tgClient, tgReady: tgReady, groupID: cfg.Telegram.GroupID}
	server := NewS3Server(storage, cfg.Server, botState, cfg.Bot.Token, cfg.Bot.Admins, cfg.Telegram.GroupID)

	if cfg.Bot.Token != "" { go server.startBot(context.Background()) }

	addr := fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)
	fmt.Printf("🌐 http://%s\n", addr)
	fmt.Printf("🔑 %s / %s\n", cfg.Server.AccessKey, cfg.Server.SecretKey)
	if cfg.Bot.Token != "" { fmt.Println("🤖 Bot on") }

	if err := server.Start(addr); err != nil { log.Fatalf("Server: %v", err) }
}
