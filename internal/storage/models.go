package storage

import (
	"encoding/json"
	"time"
)

type Source struct {
	CreatedAt   time.Time
	ActivatedAt time.Time
	URL         string
	Username    string
	Title       string
	Status      string
	LastError   string
	ID          int64
	PeerID      int64
	AccessHash  int64
	Private     bool
	Enabled     bool
}

type Message struct {
	PublishedAt     time.Time
	NextAttemptAt   time.Time
	ReceivedAt      time.Time
	LastError       string
	Status          string
	Text            string
	Media           json.RawMessage
	Classification  json.RawMessage
	DestinationMsgs json.RawMessage
	ID              int64
	GroupedID       int64
	TelegramMsgID   int
	Attempts        int
	TelegramChatID  int64
	SourceID        int64
}

type MediaItem struct {
	Type       string `json:"type"`
	FileID     string `json:"file_id,omitempty"`
	UniqueID   string `json:"unique_id,omitempty"`
	RemoteID   string `json:"remote_id,omitempty"`
	ThumbSize  string `json:"thumb_size,omitempty"`
	MIME       string `json:"mime,omitempty"`
	Name       string `json:"name,omitempty"`
	FileRef    []byte `json:"file_ref,omitempty"`
	AccessHash int64  `json:"access_hash,omitempty"`
	Size       int64  `json:"size,omitempty"`
}

type Fingerprint struct {
	CreatedAt   time.Time
	TextHash    string
	MediaUnique string
	MessageID   int64
	TextSimHash uint64
	ImageHash   uint64
}

type FeedbackExample struct {
	CreatedAt time.Time
	Text      string
	Label     string
	Topic     string
	Source    string
	ID        int64
}

type Topic struct {
	CreatedAt time.Time
	Name      string
	ID        int64
	Enabled   bool
}

type QuarantineItem struct {
	CreatedAt      time.Time
	ResolvedAt     *time.Time
	Resolution     string
	Text           string
	Reason         string
	SourceTitle    string
	SourceURL      string
	Evidence       json.RawMessage
	Classification json.RawMessage
	Confidence     float64
	MessageID      int64
	ID             int64
	TelegramMsgID  int
}
