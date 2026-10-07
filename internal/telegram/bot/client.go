package bot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/schroding3rscat/Telegram-News-Reader/internal/storage"
	tguser "github.com/schroding3rscat/Telegram-News-Reader/internal/telegram/user"
)

type Client struct {
	http    *http.Client
	baseURL string
	token   string
	target  string
}

type PublishedMessage struct {
	Mode      string `json:"mode"`
	MessageID int    `json:"message_id"`
}

type apiResponse[T any] struct {
	Result      T      `json:"result"`
	Description string `json:"description"`
	ErrorCode   int    `json:"error_code"`
	OK          bool   `json:"ok"`
}

const (
	botRequestTimeout = 90 * time.Second
	maxCaptionLength  = 1024
	maxAlbumFiles     = 10
	maxAPIResponse    = 4 << 20
)

func New(baseURL, token, target string) (*Client, error) {
	if token == "" || target == "" {
		return nil, errors.New("bot token and target channel are required")
	}
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		target:  target,
		http:    &http.Client{Timeout: botRequestTimeout},
	}, nil
}

func (c *Client) Validate(ctx context.Context) error {
	var me apiResponse[struct {
		ID int64 `json:"id"`
	}]
	if err := c.callJSON(ctx, "getMe", map[string]any{}, &me); err != nil {
		return err
	}
	if !me.OK {
		return apiError(me.ErrorCode, me.Description)
	}
	var member apiResponse[struct {
		Status          string `json:"status"`
		CanPostMessages bool   `json:"can_post_messages"`
	}]
	if err := c.callJSON(ctx, "getChatMember", map[string]any{
		"chat_id": c.target, "user_id": me.Result.ID,
	}, &member); err != nil {
		return err
	}
	if !member.OK {
		return apiError(member.ErrorCode, member.Description)
	}
	if member.Result.Status != "creator" && (member.Result.Status != "administrator" || !member.Result.CanPostMessages) {
		return errors.New("bot must be an administrator with permission to post messages")
	}
	return nil
}

func (c *Client) Forward(ctx context.Context, message storage.Message, source storage.Source) ([]PublishedMessage, error) {
	from := source.Username
	if from != "" {
		from = "@" + strings.TrimPrefix(from, "@")
	} else {
		from = botChannelID(message.TelegramChatID)
	}
	var response apiResponse[telegramMessage]
	err := c.callJSON(ctx, "forwardMessage", map[string]any{
		"chat_id":      c.target,
		"from_chat_id": from,
		"message_id":   message.TelegramMsgID,
	}, &response)
	if err != nil {
		return nil, err
	}
	if !response.OK {
		return nil, apiError(response.ErrorCode, response.Description)
	}
	if err := c.attachFeedback(ctx, response.Result.MessageID, message.ID); err != nil {
		return nil, err
	}
	return []PublishedMessage{{MessageID: response.Result.MessageID, Mode: "forward"}}, nil
}

func (c *Client) ForwardBatch(ctx context.Context, messages []storage.Message, source storage.Source) ([]PublishedMessage, error) {
	if len(messages) == 0 {
		return nil, errors.New("empty forward batch")
	}
	if len(messages) == 1 {
		return c.Forward(ctx, messages[0], source)
	}
	from := source.Username
	if from != "" {
		from = "@" + strings.TrimPrefix(from, "@")
	} else {
		from = botChannelID(messages[0].TelegramChatID)
	}
	ids := make([]int, len(messages))
	for index := range messages {
		ids[index] = messages[index].TelegramMsgID
	}
	var response apiResponse[[]telegramMessage]
	err := c.callJSON(ctx, "forwardMessages", map[string]any{
		"chat_id":      c.target,
		"from_chat_id": from,
		"message_ids":  ids,
	}, &response)
	if err != nil {
		return nil, err
	}
	if !response.OK || len(response.Result) == 0 {
		return nil, apiError(response.ErrorCode, response.Description)
	}
	lastID := response.Result[len(response.Result)-1].MessageID
	if err := c.attachFeedback(ctx, lastID, messages[0].ID); err != nil {
		return nil, err
	}
	result := make([]PublishedMessage, 0, len(response.Result))
	for index := range response.Result {
		result = append(result, PublishedMessage{MessageID: response.Result[index].MessageID, Mode: "forward"})
	}
	return result, nil
}

func (c *Client) Copy(ctx context.Context, message storage.Message, source storage.Source, files []tguser.DownloadedFile) ([]PublishedMessage, error) {
	sourceLine := "Источник: " + source.Title
	if source.URL != "" && !source.Private {
		sourceLine += " (" + source.URL + ")"
	}
	text := strings.TrimSpace(message.Text)
	if text != "" {
		text += "\n\n"
	}
	text += sourceLine
	if len(files) == 0 {
		return c.sendText(ctx, text, message.ID)
	}
	if len(files) == 1 {
		return c.sendSingleMedia(ctx, text, message.ID, files[0])
	}
	for index := range files {
		file := &files[index]
		if file.Type != "photo" && file.Type != "video" {
			result, err := c.sendText(ctx, text, message.ID)
			if err != nil {
				return nil, err
			}
			for attachmentIndex := range files {
				sentMessages, sendErr := c.sendSingleMedia(ctx, "", 0, files[attachmentIndex])
				if sendErr != nil {
					return nil, sendErr
				}
				result = append(result, sentMessages...)
			}
			return result, nil
		}
	}
	return c.sendAlbum(ctx, text, message.ID, files)
}

func (c *Client) sendText(ctx context.Context, text string, sourceMessageID int64) ([]PublishedMessage, error) {
	var response apiResponse[telegramMessage]
	err := c.callJSON(ctx, "sendMessage", map[string]any{
		"chat_id":      c.target,
		"text":         text,
		"reply_markup": feedbackMarkup(sourceMessageID),
	}, &response)
	if err != nil {
		return nil, err
	}
	if !response.OK {
		return nil, apiError(response.ErrorCode, response.Description)
	}
	return []PublishedMessage{{MessageID: response.Result.MessageID, Mode: "copy"}}, nil
}

func (c *Client) sendSingleMedia(ctx context.Context, caption string, sourceMessageID int64, file tguser.DownloadedFile) ([]PublishedMessage, error) {
	method, field := "sendDocument", "document"

	switch file.Type {
	case "photo":
		method, field = "sendPhoto", "photo"
	case "video":
		method, field = "sendVideo", "video"
	case "voice":
		method, field = "sendVoice", "voice"
	}
	if len(caption) > maxCaptionLength {
		messages, err := c.sendText(ctx, caption, sourceMessageID)
		if err != nil {
			return nil, err
		}
		caption = ""
		mediaMessage, err := c.upload(ctx, method, field, caption, 0, file)
		if err != nil {
			return nil, err
		}
		messages = append(messages, PublishedMessage{MessageID: mediaMessage, Mode: "copy"})
		return messages, nil
	}
	id, err := c.upload(ctx, method, field, caption, sourceMessageID, file)
	if err != nil {
		return nil, err
	}
	return []PublishedMessage{{MessageID: id, Mode: "copy"}}, nil
}

func (c *Client) upload(ctx context.Context, method, field, caption string, sourceMessageID int64, file tguser.DownloadedFile) (int, error) {
	values := map[string]string{
		"chat_id": c.target,
		"caption": caption,
	}
	if sourceMessageID != 0 {
		markup, err := json.Marshal(feedbackMarkup(sourceMessageID))
		if err != nil {
			return 0, fmt.Errorf("encode feedback markup: %w", err)
		}
		values["reply_markup"] = string(markup)
	}
	var response apiResponse[telegramMessage]
	if err := c.callMultipart(ctx, method, values, map[string]tguser.DownloadedFile{field: file}, &response); err != nil {
		return 0, err
	}
	if !response.OK {
		return 0, apiError(response.ErrorCode, response.Description)
	}
	return response.Result.MessageID, nil
}

func (c *Client) sendAlbum(ctx context.Context, caption string, sourceMessageID int64, files []tguser.DownloadedFile) ([]PublishedMessage, error) {
	if len(files) > maxAlbumFiles {
		files = files[:maxAlbumFiles]
	}
	attachments := make(map[string]tguser.DownloadedFile, len(files))
	media := make([]map[string]any, 0, len(files))
	for index := range files {
		file := &files[index]
		field := "file" + strconv.Itoa(index)
		kind := file.Type
		if kind != "photo" && kind != "video" {
			kind = "document"
		}
		item := map[string]any{"type": kind, "media": "attach://" + field}
		if index == 0 && len(caption) <= maxCaptionLength {
			item["caption"] = caption
		}
		media = append(media, item)
		attachments[field] = *file
	}
	mediaJSON, err := json.Marshal(media)
	if err != nil {
		return nil, fmt.Errorf("encode media group: %w", err)
	}
	var response apiResponse[[]telegramMessage]
	if err := c.callMultipart(ctx, "sendMediaGroup", map[string]string{
		"chat_id": c.target,
		"media":   string(mediaJSON),
	}, attachments, &response); err != nil {
		return nil, err
	}
	if !response.OK || len(response.Result) == 0 {
		return nil, apiError(response.ErrorCode, response.Description)
	}
	result := make([]PublishedMessage, 0, len(response.Result)+1)
	for index := range response.Result {
		result = append(result, PublishedMessage{MessageID: response.Result[index].MessageID, Mode: "copy"})
	}
	lastID := response.Result[len(response.Result)-1].MessageID
	if err := c.attachFeedback(ctx, lastID, sourceMessageID); err != nil {
		return nil, err
	}
	if len(caption) > maxCaptionLength {
		textMessages, err := c.sendText(ctx, caption, sourceMessageID)
		if err != nil {
			return nil, err
		}
		result = append(result, textMessages...)
	}
	return result, nil
}

func (c *Client) attachFeedback(ctx context.Context, targetMessageID int, sourceMessageID int64) error {
	var response apiResponse[telegramMessage]
	if err := c.callJSON(ctx, "editMessageReplyMarkup", map[string]any{
		"chat_id":      c.target,
		"message_id":   targetMessageID,
		"reply_markup": feedbackMarkup(sourceMessageID),
	}, &response); err != nil {
		return err
	}
	if !response.OK {
		return apiError(response.ErrorCode, response.Description)
	}
	return nil
}

func feedbackMarkup(messageID int64) map[string]any {
	return map[string]any{
		"inline_keyboard": [][]map[string]string{{
			{"text": "Это реклама", "callback_data": "ad:" + strconv.FormatInt(messageID, 10)},
			{"text": "Тема не интересна", "callback_data": "topic:" + strconv.FormatInt(messageID, 10)},
		}},
	}
}

func (c *Client) callJSON(ctx context.Context, method string, payload any, out any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.methodURL(method), bytes.NewReader(data))
	if err != nil {
		return err
	}

	request.Header.Set("Content-Type", "application/json")
	return c.do(request, out)
}

func (c *Client) callMultipart(ctx context.Context, method string, values map[string]string, files map[string]tguser.DownloadedFile, out any) error {
	reader, pipeWriter := io.Pipe()
	writer := multipart.NewWriter(pipeWriter)
	contentType := writer.FormDataContentType()
	writeResult := make(chan error, 1)
	go func() {
		var writeErr error
		defer func() {
			if closeErr := writer.Close(); writeErr == nil {
				writeErr = closeErr
			}

			_ = pipeWriter.CloseWithError(writeErr)
			writeResult <- writeErr
		}()
		for key, value := range values {
			if writeErr = writer.WriteField(key, value); writeErr != nil {
				return
			}
		}
		for field := range files {
			file := files[field]
			var source *os.File
			// #my-custom-nosec G304 -- file paths originate from the trusted MTProto downloader.
			source, writeErr = os.Open(file.Path)
			if writeErr != nil {
				return
			}
			header := make(textproto.MIMEHeader)
			header.Set("Content-Disposition", fmt.Sprintf(`form-data; name="%s"; filename="%s"`, field, filepath.Base(file.Name)))
			if file.MIME != "" {
				header.Set("Content-Type", file.MIME)
			} else {
				header.Set("Content-Type", "application/octet-stream")
			}
			var part io.Writer
			part, writeErr = writer.CreatePart(header)
			if writeErr == nil {
				_, writeErr = io.Copy(part, source)
			}
			_ = source.Close()
			if writeErr != nil {
				return
			}
		}
	}()
	defer func() { _ = reader.Close() }()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.methodURL(method), reader)
	if err != nil {
		return err
	}

	request.Header.Set("Content-Type", contentType)
	requestErr := c.do(request, out)
	_ = reader.CloseWithError(requestErr)
	writeErr := <-writeResult
	if requestErr != nil {
		return requestErr
	}
	return writeErr
}

func (c *Client) do(request *http.Request, out any) error {
	response, err := c.http.Do(request)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxAPIResponse))
	if err != nil {
		return err
	}
	if decodeErr := json.Unmarshal(body, out); decodeErr != nil {
		return fmt.Errorf("decode Bot API response: %w", decodeErr)
	}
	return nil
}

func (c *Client) methodURL(method string) string {
	return c.baseURL + "/bot" + c.token + "/" + method
}

func botChannelID(id int64) string {
	return "-100" + strconv.FormatInt(id, 10)
}

func apiError(code int, description string) error {
	return fmt.Errorf("bot API error %d: %s", code, description)
}

type telegramMessage struct {
	MessageID int `json:"message_id"`
}
