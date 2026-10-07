package config

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Admin        AdminConfig    `yaml:"admin"`
	Listen       string         `yaml:"listen"`
	DataDir      string         `yaml:"data_dir"`
	DatabasePath string         `yaml:"database_path"`
	Telegram     TelegramConfig `yaml:"telegram"`
	Log          LogConfig      `yaml:"log"`
	LLM          LLMConfig      `yaml:"llm"`
	Pipeline     PipelineConfig `yaml:"pipeline"`
}

type AdminConfig struct {
	Username   string `yaml:"username"`
	Password   string `yaml:"password_bcrypt"`
	QueryToken string `yaml:"query_token"`
	PublicURL  string `yaml:"public_url"`
}

type TelegramConfig struct {
	AppHash          string   `yaml:"app_hash"`
	Phone            string   `yaml:"phone"`
	SessionPath      string   `yaml:"session_path"`
	BotToken         string   `yaml:"bot_token"`
	TargetChannel    string   `yaml:"target_channel"`
	BotAPIBaseURL    string   `yaml:"bot_api_base_url"`
	UserAPIStorage   string   `yaml:"user_api_storage"`
	FeedbackUserIDs  []int64  `yaml:"feedback_user_ids"`
	DisabledFeatures []string `yaml:"disabled_features"`
	AppID            int      `yaml:"app_id"`
}

type LLMConfig struct {
	BaseURL         string        `yaml:"base_url"`
	Model           string        `yaml:"model"`
	Timeout         time.Duration `yaml:"timeout"`
	MaxExamples     int           `yaml:"max_examples"`
	AdThreshold     float64       `yaml:"ad_threshold"`
	ReviewThreshold float64       `yaml:"review_threshold"`
}

type PipelineConfig struct {
	Workers             int           `yaml:"workers"`
	PollInterval        time.Duration `yaml:"poll_interval"`
	RetryBase           time.Duration `yaml:"retry_base"`
	Retention           time.Duration `yaml:"retention"`
	AlbumSettleDelay    time.Duration `yaml:"album_settle_delay"`
	TextDistance        int           `yaml:"text_hamming_distance"`
	ImageDistance       int           `yaml:"image_hamming_distance"`
	MaxQueueDepth       int           `yaml:"max_queue_depth"`
	ClassificationBatch int           `yaml:"classification_batch"`
}

type LogConfig struct {
	Level string `yaml:"level"`
	JSON  bool   `yaml:"json"`
}

const (
	defaultLLMTimeout        = 45 * time.Second
	defaultMaxExamples       = 6
	defaultAdThreshold       = 0.78
	defaultReviewThreshold   = 0.52
	defaultPollInterval      = 2 * time.Second
	defaultRetryBase         = 10 * time.Second
	defaultRetention         = 7 * 24 * time.Hour
	defaultAlbumSettleDelay  = 2 * time.Second
	defaultTextDistance      = 7
	defaultImageDistance     = 8
	defaultMaxQueueDepth     = 100_000
	requiredQueryTokenLength = 24
	lowMemoryWorkerCount     = 1
	maximumConfidence        = 1.0
	privateDirectoryMode     = 0o700
)

func Default() Config {
	return Config{
		Listen:       "127.0.0.1:8080",
		DataDir:      "/var/lib/telegram-news-reader",
		DatabasePath: "/var/lib/telegram-news-reader/reader.db",
		Telegram: TelegramConfig{
			SessionPath:   "/var/lib/telegram-news-reader/telegram.session",
			BotAPIBaseURL: "https://api.telegram.org",
		},
		LLM: LLMConfig{
			BaseURL:         "http://127.0.0.1:8081",
			Model:           "qwen3-0.6b",
			Timeout:         defaultLLMTimeout,
			MaxExamples:     defaultMaxExamples,
			AdThreshold:     defaultAdThreshold,
			ReviewThreshold: defaultReviewThreshold,
		},
		Pipeline: PipelineConfig{
			Workers:             lowMemoryWorkerCount,
			PollInterval:        defaultPollInterval,
			RetryBase:           defaultRetryBase,
			Retention:           defaultRetention,
			AlbumSettleDelay:    defaultAlbumSettleDelay,
			TextDistance:        defaultTextDistance,
			ImageDistance:       defaultImageDistance,
			MaxQueueDepth:       defaultMaxQueueDepth,
			ClassificationBatch: lowMemoryWorkerCount,
		},
		Log: LogConfig{Level: "info"},
	}
}

func Load(path string) (Config, error) {
	cfg := Default()
	data, err := readConfigFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse config: %w", err)
	}

	cfg.applyRelativePaths(filepath.Dir(path))
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c *Config) Validate() error {
	var errs []error
	if c.Listen == "" {
		errs = append(errs, errors.New("listen is required"))
	} else if host, _, err := net.SplitHostPort(c.Listen); err != nil {
		errs = append(errs, fmt.Errorf("invalid listen address: %w", err))
	} else if host != "127.0.0.1" && host != "localhost" && host != "::1" {
		errs = append(errs, errors.New("listen must use a loopback address; expose the service through Caddy"))
	}
	if c.DataDir == "" {
		errs = append(errs, errors.New("data_dir is required"))
	}
	if c.Admin.Username == "" {
		errs = append(errs, errors.New("admin.username is required"))
	}
	if !strings.HasPrefix(c.Admin.Password, "$2") {
		errs = append(errs, errors.New("admin.password_bcrypt must contain a bcrypt hash"))
	}
	if len(c.Admin.QueryToken) < requiredQueryTokenLength {
		errs = append(errs, errors.New("admin.query_token must be at least 24 characters"))
	}
	if c.Pipeline.Workers != lowMemoryWorkerCount {
		errs = append(errs, errors.New("pipeline.workers must be 1 for the low-memory profile"))
	}
	if c.LLM.AdThreshold <= c.LLM.ReviewThreshold || c.LLM.AdThreshold > maximumConfidence ||
		c.LLM.ReviewThreshold < 0 {
		errs = append(errs, errors.New("invalid LLM confidence thresholds"))
	}
	return errors.Join(errs...)
}

func (c *Config) EnsureDataDir() error {
	if err := os.MkdirAll(c.DataDir, privateDirectoryMode); err != nil {
		return fmt.Errorf("create data directory: %w", err)
	}
	return os.Chmod(c.DataDir, privateDirectoryMode)
}

func (c *Config) applyRelativePaths(base string) {
	if c.DataDir != "" && !filepath.IsAbs(c.DataDir) {
		c.DataDir = filepath.Join(base, c.DataDir)
	}
	if c.DatabasePath == "" {
		c.DatabasePath = filepath.Join(c.DataDir, "reader.db")
	} else if !filepath.IsAbs(c.DatabasePath) {
		c.DatabasePath = filepath.Join(base, c.DatabasePath)
	}
	if c.Telegram.SessionPath == "" {
		c.Telegram.SessionPath = filepath.Join(c.DataDir, "telegram.session")
	} else if !filepath.IsAbs(c.Telegram.SessionPath) {
		c.Telegram.SessionPath = filepath.Join(base, c.Telegram.SessionPath)
	}
}

func readConfigFile(path string) ([]byte, error) {
	absolutePath, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(filepath.Dir(absolutePath))
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	return root.ReadFile(filepath.Base(absolutePath))
}
