package pipeline

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"time"

	"github.com/schroding3rscat/Telegram-News-Reader/internal/classifier"
	"github.com/schroding3rscat/Telegram-News-Reader/internal/dedup"
	"github.com/schroding3rscat/Telegram-News-Reader/internal/storage"
	"github.com/schroding3rscat/Telegram-News-Reader/internal/telegram/bot"
	tguser "github.com/schroding3rscat/Telegram-News-Reader/internal/telegram/user"
)

type Classifier interface {
	Classify(ctx context.Context, text string, topics []storage.Topic) (classifier.Result, error)
}

type Downloader interface {
	DownloadMedia(ctx context.Context, item storage.MediaItem) (tguser.DownloadedFile, error)
	CleanupDownload(file tguser.DownloadedFile)
}

type Options struct {
	PollInterval     time.Duration
	RetryBase        time.Duration
	Retention        time.Duration
	AlbumSettleDelay time.Duration
	AdThreshold      float64
	ReviewThreshold  float64
}

type Pipeline struct {
	classifier Classifier
	downloader Downloader
	store      *storage.Store
	dedup      *dedup.Detector
	publisher  *bot.Client
	logger     *slog.Logger
	options    Options
}

type duplicateClassification struct {
	Reason      string `json:"reason"`
	DuplicateOf int64  `json:"duplicate_of"`
}

const (
	maxRetryExponent = 8
	retryMultiplier  = 2
)

func New(store *storage.Store, detector *dedup.Detector, class Classifier, publisher *bot.Client,
	downloader Downloader, options Options, logger *slog.Logger,
) *Pipeline {
	if logger == nil {
		logger = slog.Default()
	}
	return &Pipeline{
		store: store, dedup: detector, classifier: class, publisher: publisher,
		downloader: downloader, options: options, logger: logger,
	}
}

func (p *Pipeline) Run(ctx context.Context) error {
	poll := time.NewTicker(p.options.PollInterval)
	defer poll.Stop()
	cleanup := time.NewTicker(time.Hour)
	defer cleanup.Stop()

	for {
		messages, err := p.store.ClaimNextMessages(ctx, p.options.AlbumSettleDelay)
		if err == nil {
			p.process(ctx, messages)
			continue
		}
		if !errors.Is(err, sql.ErrNoRows) {
			p.logger.Error("claim pipeline message", "error", err)
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-cleanup.C:
			if _, err := p.store.Cleanup(ctx, time.Now().Add(-p.options.Retention)); err != nil {
				p.logger.Warn("pipeline cleanup failed", "error", err)
			}
		case <-poll.C:
		}
	}
}

func (p *Pipeline) ProcessOnce(ctx context.Context) error {
	messages, err := p.store.ClaimNextMessages(ctx, p.options.AlbumSettleDelay)
	if err != nil {
		return err
	}
	if processErr := p.processBatch(ctx, messages); processErr != nil {
		return fmt.Errorf("process batch: %w", processErr)
	}
	return nil
}

func (p *Pipeline) process(ctx context.Context, messages []storage.Message) {
	if err := p.processBatch(ctx, messages); err != nil {
		p.logger.Warn("pipeline batch failed", "message_id", messages[0].ID, "error", err)
		for index := range messages {
			message := &messages[index]
			delay := retryDelay(p.options.RetryBase, message.Attempts)
			_ = p.store.RetryMessage(ctx, message.ID, err.Error(), delay)
		}
	}
}

func (p *Pipeline) processBatch(ctx context.Context, messages []storage.Message) error {
	source, err := p.store.SourceByID(ctx, messages[0].SourceID)
	if err != nil {
		return err
	}
	text, mediaItems := combine(messages)
	mediaKeys := make([]string, 0, len(mediaItems))
	for index := range mediaItems {
		item := &mediaItems[index]
		if item.UniqueID != "" {
			mediaKeys = append(mediaKeys, item.UniqueID)
		} else if item.RemoteID != "" {
			mediaKeys = append(mediaKeys, item.Type+":"+item.RemoteID)
		}
	}
	match, err := p.dedup.Check(ctx, messages[0].ID, dedup.Candidate{
		Text: text, MediaUnique: mediaKeys,
	})
	if err != nil {
		return err
	}
	if match.DuplicateOf != 0 {
		data, _ := json.Marshal(duplicateClassification{
			DuplicateOf: match.DuplicateOf,
			Reason:      match.Reason,
		})
		for index := range messages {
			message := &messages[index]
			if markErr := p.store.MarkMessage(ctx, message.ID, "duplicate", data, nil); markErr != nil {
				return markErr
			}
		}
		return nil
	}

	topics, err := p.store.ListTopics(ctx)
	if err != nil {
		return err
	}
	result, err := p.classifier.Classify(ctx, text, topics)
	if err != nil {
		return err
	}
	classification, marshalErr := json.Marshal(result)
	if marshalErr != nil {
		return fmt.Errorf("encode classification: %w", marshalErr)
	}
	if result.IsUninteresting {
		for index := range messages {
			message := &messages[index]
			if markErr := p.store.MarkMessage(ctx, message.ID, "ignored", classification, nil); markErr != nil {
				return markErr
			}
		}
		return p.store.AddFingerprint(ctx, match.Fingerprint)
	}
	reason := ""

	switch {
	case result.IsAd && result.Confidence >= p.options.AdThreshold:
		reason = "advertising"
	case result.IsAd:
		reason = "needs_review"
	case result.Confidence < p.options.ReviewThreshold:
		reason = "needs_review"
	}
	if reason != "" {
		for index := range messages {
			message := &messages[index]
			if quarantineErr := p.store.AddQuarantine(
				ctx,
				message.ID,
				reason,
				result.Confidence,
				result.EvidenceSpans,
			); quarantineErr != nil {
				return quarantineErr
			}
			if markErr := p.store.MarkMessage(
				ctx,
				message.ID,
				"quarantined",
				classification,
				nil,
			); markErr != nil {
				return markErr
			}
		}
		return p.store.AddFingerprint(ctx, match.Fingerprint)
	}

	published, err := p.publish(ctx, messages, source, mediaItems)
	if err != nil {
		return err
	}
	destinations, _ := json.Marshal(published)
	for index := range messages {
		message := &messages[index]
		if markErr := p.store.MarkMessage(ctx, message.ID, "published", classification, destinations); markErr != nil {
			return markErr
		}
		if index < len(published) {
			if publicationErr := p.store.AddPublication(
				ctx,
				message.ID,
				0,
				published[index].MessageID,
				published[index].Mode,
			); publicationErr != nil {
				return publicationErr
			}
		}
	}
	return p.store.AddFingerprint(ctx, match.Fingerprint)
}

func (p *Pipeline) publish(ctx context.Context, messages []storage.Message, source storage.Source,
	mediaItems []storage.MediaItem,
) ([]bot.PublishedMessage, error) {
	if source.Username != "" && !source.Private {
		published, err := p.publisher.ForwardBatch(ctx, messages, source)
		if err == nil {
			return published, nil
		}

		p.logger.Warn("public forward failed, falling back to copy", "source", source.Username, "error", err)
	}
	if p.downloader == nil && len(mediaItems) > 0 {
		return nil, errors.New("media downloader is unavailable for copy fallback")
	}
	files := make([]tguser.DownloadedFile, 0, len(mediaItems))
	for index := range mediaItems {
		item := &mediaItems[index]
		file, err := p.downloader.DownloadMedia(ctx, *item)
		if err != nil {
			for fileIndex := range files {
				p.downloader.CleanupDownload(files[fileIndex])
			}
			return nil, err
		}
		files = append(files, file)
	}
	defer func() {
		for index := range files {
			p.downloader.CleanupDownload(files[index])
		}
	}()
	combined := messages[0]
	combined.Text, _ = combine(messages)
	return p.publisher.Copy(ctx, combined, source, files)
}

func combine(messages []storage.Message) (string, []storage.MediaItem) {
	var texts []string
	var media []storage.MediaItem
	for index := range messages {
		message := &messages[index]
		if value := strings.TrimSpace(message.Text); value != "" {
			texts = append(texts, value)
		}
		var items []storage.MediaItem
		if json.Unmarshal(message.Media, &items) == nil {
			media = append(media, items...)
		}
	}
	return strings.Join(texts, "\n\n"), media
}

func retryDelay(base time.Duration, attempts int) time.Duration {
	exponent := math.Min(float64(attempts), maxRetryExponent)
	delay := time.Duration(float64(base) * math.Pow(retryMultiplier, exponent))
	if delay > time.Hour {
		return time.Hour
	}
	return delay
}
