package dedup

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/schroding3rscat/Telegram-News-Reader/internal/storage"
)

func TestNormalizeAndSimilarDuplicate(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	sourceID, err := store.AddSource(ctx, storage.Source{URL: "@source", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	firstID, err := store.EnqueueMessage(ctx, storage.Message{
		SourceID: sourceID, TelegramChatID: 1, TelegramMsgID: 1,
		Text: "Большая новость произошла сегодня в Москве! https://example.com @source",
	})
	if err != nil {
		t.Fatal(err)
	}
	secondID, err := store.EnqueueMessage(ctx, storage.Message{
		SourceID: sourceID, TelegramChatID: 1, TelegramMsgID: 2,
		Text: "Большая новость произошла сегодня в Москве.",
	})
	if err != nil {
		t.Fatal(err)
	}
	detector := New(store, 7*24*time.Hour, 8, 8)
	first, err := detector.Check(ctx, firstID, Candidate{Text: "Большая новость произошла сегодня в Москве! https://example.com @source"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AddFingerprint(ctx, first.Fingerprint); err != nil {
		t.Fatal(err)
	}
	second, err := detector.Check(ctx, secondID, Candidate{Text: "Большая новость произошла сегодня в Москве."})
	if err != nil {
		t.Fatal(err)
	}
	if second.DuplicateOf != firstID {
		t.Fatalf("expected duplicate of %d, got %+v", firstID, second)
	}
}

func TestHamming(t *testing.T) {
	t.Parallel()
	if got := Hamming(0b1010, 0b0011); got != 2 {
		t.Fatalf("got %d", got)
	}
}
