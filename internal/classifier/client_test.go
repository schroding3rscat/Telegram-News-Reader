package classifier

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/schroding3rscat/Telegram-News-Reader/internal/storage"
)

func TestParseResultValidatesEvidence(t *testing.T) {
	t.Parallel()
	source := "Купите подписку со скидкой по промокоду NEWS."
	result, err := parseResult(`{
		"is_ad": true,
		"confidence": 0.96,
		"topic": "подписки",
		"is_uninteresting": false,
		"evidence_spans": ["Купите подписку", "промокоду NEWS"],
		"reason": "прямая продажа"
	}`, source)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsAd || result.Confidence != 0.96 {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestParseResultRejectsHallucinatedEvidence(t *testing.T) {
	t.Parallel()
	_, err := parseResult(`{
		"is_ad": true,
		"confidence": 0.9,
		"topic": "товары",
		"is_uninteresting": false,
		"evidence_spans": ["этого текста нет"],
		"reason": "реклама"
	}`, "Обычная новость")
	if err == nil {
		t.Fatal("expected invalid evidence to be rejected")
	}
}

func TestClassifyRecordsLatency(t *testing.T) {
	t.Parallel()
	store, err := storage.Open(filepath.Join(t.TempDir(), "classifier.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	api := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]string{"role": "assistant", "content": `{
					"is_ad": false,
					"confidence": 0.91,
					"topic": "news",
					"is_uninteresting": false,
					"evidence_spans": [],
					"reason": "обычная новость"
				}`}},
			},
		})
	}))
	t.Cleanup(api.Close)
	client, err := New(api.URL, "qwen", time.Second, 1, store)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Classify(t.Context(), "Сегодня вышла обычная новость.", nil); err != nil {
		t.Fatal(err)
	}
	dashboard, err := store.Dashboard(t.Context(), 1, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if dashboard.LatencyCalls != 1 {
		t.Fatalf("expected LLM latency sample, got %+v", dashboard)
	}
}

func TestCheapSignals(t *testing.T) {
	t.Parallel()
	signals := CheapSignals("Реклама. Используйте промокод NEWS")
	if len(signals) < 2 {
		t.Fatalf("expected ad signals, got %v", signals)
	}
}
