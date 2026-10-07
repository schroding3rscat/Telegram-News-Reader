package bot

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/schroding3rscat/Telegram-News-Reader/internal/classifier"
	"github.com/schroding3rscat/Telegram-News-Reader/internal/storage"
)

type CallbackProcessor struct {
	bot     *Client
	store   *storage.Store
	allowed map[int64]struct{}
	logger  *slog.Logger
}

const (
	callbackLongPollSeconds = 30
	callbackRetryDelay      = 5 * time.Second
	confirmedConfidence     = 1.0
)

func NewCallbackProcessor(bot *Client, store *storage.Store, allowedUserIDs []int64, logger *slog.Logger) *CallbackProcessor {
	allowed := make(map[int64]struct{}, len(allowedUserIDs))
	for _, id := range allowedUserIDs {
		allowed[id] = struct{}{}
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &CallbackProcessor{bot: bot, store: store, allowed: allowed, logger: logger}
}

func (p *CallbackProcessor) Run(ctx context.Context) error {
	offset := p.loadOffset(ctx)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		var response apiResponse[[]botUpdate]
		err := p.bot.callJSON(ctx, "getUpdates", map[string]any{
			"offset":          offset,
			"timeout":         callbackLongPollSeconds,
			"allowed_updates": []string{"callback_query"},
		}, &response)
		if err != nil || !response.OK {
			if err == nil {
				err = apiError(response.ErrorCode, response.Description)
			}

			p.logger.Warn("callback polling failed", "error", err)

			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(callbackRetryDelay):
				continue
			}
		}
		for _, update := range response.Result {
			if update.UpdateID >= offset {
				offset = update.UpdateID + 1
			}
			if update.CallbackQuery == nil {
				continue
			}
			if err := p.handle(ctx, *update.CallbackQuery); err != nil {
				p.logger.Warn("callback handling failed", "error", err)
			}
		}
		_ = p.store.PutSetting(ctx, "bot.update_offset", strconv.Itoa(offset))
	}
}

func (p *CallbackProcessor) handle(ctx context.Context, callback callbackQuery) error {
	if _, ok := p.allowed[callback.From.ID]; !ok {
		return p.answer(ctx, callback.ID, "Недостаточно прав", true)
	}
	action, value, ok := strings.Cut(callback.Data, ":")
	if !ok {
		return p.answer(ctx, callback.ID, "Некорректная команда", true)
	}
	messageID, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return p.answer(ctx, callback.ID, "Некорректное сообщение", true)
	}
	message, err := p.store.GetMessage(ctx, messageID)
	if err != nil {
		return err
	}

	switch action {
	case "ad":
		if err := p.store.AddFeedback(ctx, storage.FeedbackExample{
			Text: message.Text, Label: "ad", Source: "telegram_button",
		}); err != nil {
			return err
		}
		if err := p.store.AddQuarantine(
			ctx,
			message.ID,
			"user_marked_ad",
			confirmedConfidence,
			nil,
		); err != nil {
			return err
		}
		return p.answer(ctx, callback.ID, "Помечено как реклама", false)
	case "topic":
		var classification classifier.Result
		_ = json.Unmarshal(message.Classification, &classification)
		topic := strings.TrimSpace(classification.Topic)
		if topic == "" {
			topic = "Тема сообщения " + strconv.FormatInt(message.ID, 10)
		}
		if err := p.store.AddTopic(ctx, topic); err != nil {
			return err
		}
		if err := p.store.AddFeedback(ctx, storage.FeedbackExample{
			Text: message.Text, Label: "uninteresting", Topic: topic, Source: "telegram_button",
		}); err != nil {
			return err
		}
		return p.answer(ctx, callback.ID, "Тема добавлена в фильтр", false)
	default:
		return p.answer(ctx, callback.ID, "Неизвестная команда", true)
	}
}

func (p *CallbackProcessor) answer(ctx context.Context, callbackID, text string, alert bool) error {
	var response apiResponse[bool]
	if err := p.bot.callJSON(ctx, "answerCallbackQuery", map[string]any{
		"callback_query_id": callbackID,
		"text":              text,
		"show_alert":        alert,
	}, &response); err != nil {
		return err
	}
	if !response.OK {
		return apiError(response.ErrorCode, response.Description)
	}
	return nil
}

func (p *CallbackProcessor) loadOffset(ctx context.Context) int {
	value, err := p.store.GetSetting(ctx, "bot.update_offset")
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		p.logger.Warn("failed to load bot offset", "error", err)
		return 0
	}
	offset, _ := strconv.Atoi(value)
	return offset
}

type botUpdate struct {
	CallbackQuery *callbackQuery `json:"callback_query"`
	UpdateID      int            `json:"update_id"`
}

type callbackQuery struct {
	ID   string `json:"id"`
	Data string `json:"data"`
	From struct {
		ID int64 `json:"id"`
	} `json:"from"`
}
