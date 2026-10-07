package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/schroding3rscat/Telegram-News-Reader/internal/classifier"
	"github.com/schroding3rscat/Telegram-News-Reader/internal/config"
	"github.com/schroding3rscat/Telegram-News-Reader/internal/storage"
)

type sample struct {
	Text  string `json:"text"`
	Topic string `json:"topic"`
	IsAd  bool   `json:"is_ad"`
}

func main() {
	configPath := flag.String("config", "config.yaml", "service config")
	datasetPath := flag.String("dataset", "testdata/classifier_golden.jsonl", "golden JSONL")

	flag.Parse()
	if err := run(*configPath, *datasetPath); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(configPath, datasetPath string) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	tempDir, err := os.MkdirTemp("", "telegram-news-reader-eval-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(tempDir) }()
	store, err := storage.Open(filepath.Join(tempDir, "eval.db"))
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()
	client, err := classifier.New(cfg.LLM.BaseURL, cfg.LLM.Model, cfg.LLM.Timeout, cfg.LLM.MaxExamples, store)
	if err != nil {
		return err
	}
	absoluteDatasetPath, err := filepath.Abs(datasetPath)
	if err != nil {
		return err
	}
	datasetRoot, err := os.OpenRoot(filepath.Dir(absoluteDatasetPath))
	if err != nil {
		return err
	}
	defer func() { _ = datasetRoot.Close() }()
	file, err := datasetRoot.Open(filepath.Base(absoluteDatasetPath))
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	var tp, fp, fn, topicCorrect, count int

	ctx := context.Background()
	scanner := bufio.NewScanner(file)

	for scanner.Scan() {
		var value sample
		if decodeErr := json.Unmarshal(scanner.Bytes(), &value); decodeErr != nil {
			return decodeErr
		}
		result, classifyErr := client.Classify(ctx, value.Text, nil)
		if classifyErr != nil {
			return fmt.Errorf("sample %d: %w", count+1, classifyErr)
		}

		switch {
		case result.IsAd && value.IsAd:
			tp++
		case result.IsAd && !value.IsAd:
			fp++
		case !result.IsAd && value.IsAd:
			fn++
		}
		if strings.EqualFold(strings.TrimSpace(result.Topic), strings.TrimSpace(value.Topic)) {
			topicCorrect++
		}

		count++
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	precision := ratio(tp, tp+fp)
	recall := ratio(tp, tp+fn)
	_, _ = fmt.Fprintf(os.Stdout, "samples=%d precision=%.3f recall=%.3f false_positive_rate=%.3f topic_accuracy=%.3f\n",
		count, precision, recall, ratio(fp, count), ratio(topicCorrect, count))
	return nil
}

func ratio(value, total int) float64 {
	if total == 0 {
		return 0
	}
	return float64(value) / float64(total)
}
