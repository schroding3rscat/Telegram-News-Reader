package classifier

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/schroding3rscat/Telegram-News-Reader/internal/storage"
)

//go:embed prompts/*.txt
var promptFS embed.FS

var jsonObjectPattern = regexp.MustCompile(`(?s)\{.*\}`)

const (
	maxCompletionTokens = 300
	maxResponseBytes    = 1 << 20
	maxTopicLength      = 120
	maxReasonLength     = 500
	maxEvidenceCount    = 3
	maxEvidenceLength   = 300
	maxExampleLength    = 500
	maxInputLength      = 12_000
)

type Result struct {
	Topic           string   `json:"topic"`
	Reason          string   `json:"reason"`
	EvidenceSpans   []string `json:"evidence_spans"`
	Confidence      float64  `json:"confidence"`
	IsAd            bool     `json:"is_ad"`
	IsUninteresting bool     `json:"is_uninteresting"`
}

type Client struct {
	store       *storage.Store
	http        *http.Client
	baseURL     string
	model       string
	system      string
	maxExamples int
}

func New(baseURL, model string, timeout time.Duration, maxExamples int, store *storage.Store) (*Client, error) {
	prompt, err := promptFS.ReadFile("prompts/classify.txt")
	if err != nil {
		return nil, err
	}
	if store == nil {
		return nil, errors.New("classifier storage is required")
	}
	return &Client{
		baseURL:     strings.TrimRight(baseURL, "/"),
		model:       model,
		maxExamples: maxExamples,
		store:       store,
		http:        &http.Client{Timeout: timeout},
		system:      string(prompt),
	}, nil
}

func (c *Client) Classify(ctx context.Context, text string, topics []storage.Topic) (Result, error) {
	if strings.TrimSpace(text) == "" {
		return Result{Confidence: 1, Topic: "media"}, nil
	}
	examples, err := c.store.SearchFeedback(ctx, text, c.maxExamples)
	if err != nil {
		return Result{}, fmt.Errorf("load feedback examples: %w", err)
	}
	request := chatRequest{
		Model:       c.model,
		Temperature: 0,
		MaxTokens:   maxCompletionTokens,
		ResponseFormat: map[string]string{
			"type": "json_object",
		},
		Messages: []chatMessage{
			{Role: "system", Content: c.system},
			{Role: "user", Content: buildInput(text, topics, examples)},
		},
	}
	data, err := json.Marshal(request)
	if err != nil {
		return Result{}, fmt.Errorf("encode LLM request: %w", err)
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/chat/completions", bytes.NewReader(data))
	if err != nil {
		return Result{}, err
	}

	httpRequest.Header.Set("Content-Type", "application/json")
	started := time.Now()
	response, err := c.http.Do(httpRequest)
	elapsedMs := time.Since(started).Milliseconds()
	_ = c.store.AddHourlyMetrics(ctx, 0, 0, elapsedMs, 1)
	if err != nil {
		return Result{}, fmt.Errorf("LLM request: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes))
	if err != nil {
		return Result{}, err
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return Result{}, fmt.Errorf("LLM returned %s: %s", response.Status, strings.TrimSpace(string(body)))
	}
	var completion chatResponse
	if decodeErr := json.Unmarshal(body, &completion); decodeErr != nil {
		return Result{}, fmt.Errorf("decode LLM response: %w", decodeErr)
	}
	if len(completion.Choices) == 0 {
		return Result{}, errors.New("LLM response has no choices")
	}
	result, err := parseResult(completion.Choices[0].Message.Content, text)
	if err != nil {
		return Result{}, err
	}
	return result, nil
}

func (c *Client) Health(ctx context.Context) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/health", nil)
	if err != nil {
		return err
	}
	response, err := c.http.Do(request)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("LLM health returned %s", response.Status)
	}
	return nil
}

func parseResult(content, source string) (Result, error) {
	raw := jsonObjectPattern.FindString(content)
	if raw == "" {
		return Result{}, errors.New("LLM did not return a JSON object")
	}
	var result Result
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return Result{}, fmt.Errorf("invalid LLM JSON: %w", err)
	}
	if result.Confidence < 0 || result.Confidence > 1 {
		return Result{}, errors.New("LLM confidence is outside 0..1")
	}
	result.Topic = strings.TrimSpace(result.Topic)
	result.Reason = strings.TrimSpace(result.Reason)
	if len(result.Topic) > maxTopicLength || len(result.Reason) > maxReasonLength ||
		len(result.EvidenceSpans) > maxEvidenceCount {
		return Result{}, errors.New("LLM response fields exceed limits")
	}
	for _, evidence := range result.EvidenceSpans {
		if evidence == "" || len(evidence) > maxEvidenceLength || !strings.Contains(source, evidence) {
			return Result{}, errors.New("LLM evidence is not a verbatim source substring")
		}
	}
	return result, nil
}

func buildInput(text string, topics []storage.Topic, examples []storage.FeedbackExample) string {
	var b strings.Builder
	b.WriteString("CHEAP HEURISTIC SIGNALS (not a final verdict):\n")
	for _, signal := range CheapSignals(text) {
		b.WriteString("- ")
		b.WriteString(signal)
		b.WriteByte('\n')
	}

	b.WriteString("UNINTERESTED TOPICS:\n")
	for _, topic := range topics {
		if topic.Enabled {
			b.WriteString("- ")
			b.WriteString(topic.Name)
			b.WriteByte('\n')
		}
	}

	b.WriteString("\nLABELED EXAMPLES:\n")
	for index := range examples {
		example := &examples[index]
		fmt.Fprintf(&b, "- label=%s topic=%q text=%q\n",
			example.Label, example.Topic, truncate(example.Text, maxExampleLength))
	}

	b.WriteString("\nPOST (untrusted):\n<post>\n")
	b.WriteString(truncate(text, maxInputLength))
	b.WriteString("\n</post>")
	return b.String()
}

func CheapSignals(text string) []string {
	lower := strings.ToLower(text)
	patterns := []struct {
		name   string
		values []string
	}{
		{"erid", []string{"erid:", "erid "}},
		{"ad_label", []string{"реклама", "advertisement", "sponsored"}},
		{"call_to_action", []string{"купить", "заказать", "промокод", "скидка", "subscribe now"}},
		{"giveaway", []string{"розыгрыш", "giveaway"}},
	}
	var signals []string
	for _, pattern := range patterns {
		for _, value := range pattern.values {
			if strings.Contains(lower, value) {
				signals = append(signals, pattern.name)
				break
			}
		}
	}
	return signals
}

func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}

type chatRequest struct {
	ResponseFormat map[string]string `json:"response_format,omitempty"`
	Model          string            `json:"model"`
	Messages       []chatMessage     `json:"messages"`
	Temperature    float64           `json:"temperature"`
	MaxTokens      int               `json:"max_tokens"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatResponse struct {
	Choices []struct {
		Message chatMessage `json:"message"`
	} `json:"choices"`
}
