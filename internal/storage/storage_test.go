package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestClaimAlbumAtomically(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openTestStore(t)
	sourceID, err := store.AddSource(ctx, Source{URL: "@album", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	for id := 1; id <= 3; id++ {
		if _, err := store.EnqueueMessage(ctx, Message{
			SourceID: sourceID, TelegramChatID: 1, TelegramMsgID: id, GroupedID: 42,
			ReceivedAt: time.Now().Add(-time.Minute),
		}); err != nil {
			t.Fatal(err)
		}
	}
	messages, err := store.ClaimNextMessages(ctx, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 3 {
		t.Fatalf("expected 3 album messages, got %d", len(messages))
	}
	if _, err := store.ClaimNextMessages(ctx, time.Second); err == nil {
		t.Fatal("album must not be claimed twice")
	}
}

func TestFeedbackSearch(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openTestStore(t)
	if err := store.AddFeedback(ctx, FeedbackExample{
		Text: "Купите курс программирования со скидкой", Label: "ad", Source: "test",
	}); err != nil {
		t.Fatal(err)
	}
	examples, err := store.SearchFeedback(ctx, "курс программирования", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(examples) != 1 || examples[0].Label != "ad" {
		t.Fatalf("unexpected examples: %+v", examples)
	}
}

func TestCleanupKeepsQuarantine(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openTestStore(t)
	sourceID, _ := store.AddSource(ctx, Source{URL: "@cleanup", Enabled: true})
	messageID, _ := store.EnqueueMessage(ctx, Message{
		SourceID: sourceID, TelegramChatID: 1, TelegramMsgID: 1,
		ReceivedAt: time.Now().Add(-8 * 24 * time.Hour),
	})
	if err := store.MarkMessage(ctx, messageID, "quarantined", nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := store.AddQuarantine(ctx, messageID, "advertising", 1, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Cleanup(ctx, time.Now().Add(-7*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetMessage(ctx, messageID); err != nil {
		t.Fatalf("quarantined message was removed: %v", err)
	}
}

func BenchmarkQueueDailyLoad(b *testing.B) {
	ctx := context.Background()
	store, err := Open(filepath.Join(b.TempDir(), "benchmark.db"))
	if err != nil {
		b.Fatal(err)
	}
	defer store.Close()
	sourceID, err := store.AddSource(ctx, Source{URL: "@benchmark", Enabled: true})
	if err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		if _, err := store.EnqueueMessage(ctx, Message{
			SourceID: sourceID, TelegramChatID: 1, TelegramMsgID: index + 1,
			Text: "Benchmark news message",
		}); err != nil {
			b.Fatal(err)
		}
	}
}
