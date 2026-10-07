package pipeline

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/schroding3rscat/Telegram-News-Reader/internal/classifier"
	"github.com/schroding3rscat/Telegram-News-Reader/internal/dedup"
	"github.com/schroding3rscat/Telegram-News-Reader/internal/storage"
	"github.com/schroding3rscat/Telegram-News-Reader/internal/telegram/bot"
)

type fakeClassifier struct{ result classifier.Result }

func (f fakeClassifier) Classify(context.Context, string, []storage.Topic) (classifier.Result, error) {
	return f.result, nil
}

func TestProcessOncePublishesExactlyOnce(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, err := storage.Open(filepath.Join(t.TempDir(), "pipeline.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	sourceID, err := store.AddSource(ctx, storage.Source{
		URL: "https://t.me/+private", Title: "Private", Private: true, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	messageID, err := store.EnqueueMessage(ctx, storage.Message{
		SourceID: sourceID, TelegramChatID: 1, TelegramMsgID: 1,
		Text: "Обычная новость без рекламы",
	})
	if err != nil {
		t.Fatal(err)
	}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok": true, "result": map[string]any{"message_id": 99},
		})
	}))
	defer api.Close()
	publisher, _ := bot.New(api.URL, "token", "@target")
	processor := New(store, dedup.New(store, 7*24*time.Hour, 7, 8),
		fakeClassifier{result: classifier.Result{Confidence: 0.99, Topic: "news"}},
		publisher, nil, Options{
			RetryBase: time.Second, Retention: 7 * 24 * time.Hour,
			ReviewThreshold: 0.5, AdThreshold: 0.8,
		}, nil)
	if err := processor.ProcessOnce(ctx); err != nil {
		t.Fatal(err)
	}
	message, err := store.GetMessage(ctx, messageID)
	if err != nil {
		t.Fatal(err)
	}
	if message.Status != "published" {
		t.Fatalf("expected published, got %s", message.Status)
	}
	if err := processor.ProcessOnce(ctx); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expected queue to be empty, got %v", err)
	}
}
