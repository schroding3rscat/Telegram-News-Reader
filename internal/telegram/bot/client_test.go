package bot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/schroding3rscat/Telegram-News-Reader/internal/storage"
)

func TestForwardAddsFeedbackKeyboard(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var methods []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		methods = append(methods, r.URL.Path)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/bottoken/forwardMessage" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok": true, "result": map[string]any{"message_id": 100},
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok": true, "result": map[string]any{"message_id": 100},
		})
	}))
	defer server.Close()
	client, err := New(server.URL, "token", "@target")
	if err != nil {
		t.Fatal(err)
	}
	published, err := client.Forward(context.Background(), storage.Message{
		ID: 7, TelegramChatID: 1, TelegramMsgID: 42,
	}, storage.Source{Username: "source"})
	if err != nil {
		t.Fatal(err)
	}
	if len(published) != 1 || published[0].MessageID != 100 {
		t.Fatalf("unexpected publication: %+v", published)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(methods) != 2 || methods[0] != "/bottoken/forwardMessage" || methods[1] != "/bottoken/editMessageReplyMarkup" {
		t.Fatalf("unexpected Bot API calls: %v", methods)
	}
}
