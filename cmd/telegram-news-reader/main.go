package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/schroding3rscat/Telegram-News-Reader/internal/classifier"
	"github.com/schroding3rscat/Telegram-News-Reader/internal/config"
	"github.com/schroding3rscat/Telegram-News-Reader/internal/dedup"
	"github.com/schroding3rscat/Telegram-News-Reader/internal/pipeline"
	"github.com/schroding3rscat/Telegram-News-Reader/internal/storage"
	"github.com/schroding3rscat/Telegram-News-Reader/internal/telegram/bot"
	tguser "github.com/schroding3rscat/Telegram-News-Reader/internal/telegram/user"
	"github.com/schroding3rscat/Telegram-News-Reader/internal/web"
)

var version = "dev"

func main() {
	if len(os.Args) > 1 && os.Args[1] == "hash-password" {
		hashPassword(os.Args[2:])
		return
	}
	if err := run(); err != nil && !errors.Is(err, context.Canceled) {
		slog.Error("service stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	flags := flag.NewFlagSet("telegram-news-reader", flag.ExitOnError)
	configPath := flags.String("config", "/etc/telegram-news-reader/config.yaml", "path to YAML config")
	showVersion := flags.Bool("version", false, "print version")
	_ = flags.Parse(os.Args[1:])
	if *showVersion {
		_, _ = fmt.Fprintln(os.Stdout, version)
		return nil
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	if ensureErr := cfg.EnsureDataDir(); ensureErr != nil {
		return ensureErr
	}
	store, err := storage.Open(cfg.DatabasePath)
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()

	logger := newLogger(cfg.Log)
	slog.SetDefault(logger)
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	setupReady := make(chan struct{})
	var setupOnce sync.Once
	restartRequested := make(chan struct{})
	var restartOnce sync.Once
	if setting(ctx, store, "setup.complete", "") == "true" {
		setupOnce.Do(func() { close(setupReady) })
	}
	proxy := newResolverProxy()
	admin, err := web.New(web.Dependencies{
		Config:   cfg,
		Store:    store,
		Resolver: proxy,
		OnConfigured: func() {
			setupOnce.Do(func() { close(setupReady) })
		},
		OnSettingsChanged: func() {
			restartOnce.Do(func() { close(restartRequested) })
		},
		Logger: logger,
	})
	if err != nil {
		return err
	}

	errorsCh := make(chan error, 8)
	go func() {
		logger.Info("admin listening", "address", cfg.Listen)

		errorsCh <- admin.Listen()
	}()
	defer func() { _ = admin.Shutdown() }()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case runErr := <-errorsCh:
		return runErr
	case <-setupReady:
	case <-restartRequested:
		return errors.New("settings changed; restarting service")
	}

	runtimeCfg, err := loadRuntimeConfig(ctx, cfg, store)
	if err != nil {
		return err
	}
	authenticator := tguser.NewStoreAuthenticator(store)
	userClient, err := tguser.New(tguser.Options{
		AppID: runtimeCfg.Telegram.AppID, AppHash: runtimeCfg.Telegram.AppHash,
		SessionPath: runtimeCfg.Telegram.SessionPath, Auth: authenticator, Store: store, Logger: logger,
		MaxQueueDepth: runtimeCfg.Pipeline.MaxQueueDepth,
	})
	if err != nil {
		return err
	}

	proxy.Set(userClient)

	botClient, err := bot.New(runtimeCfg.Telegram.BotAPIBaseURL, runtimeCfg.Telegram.BotToken,
		runtimeCfg.Telegram.TargetChannel)
	if err != nil {
		return err
	}
	validateCtx, validateCancel := context.WithTimeout(ctx, 15*time.Second)
	if validationErr := botClient.Validate(validateCtx); validationErr != nil {
		logger.Warn("bot configuration check failed", "error", validationErr)
		_ = store.PutSetting(ctx, "status.bot", validationErr.Error())
	} else {
		_ = store.PutSetting(ctx, "status.bot", "ok")
	}

	validateCancel()
	llm, err := classifier.New(runtimeCfg.LLM.BaseURL, runtimeCfg.LLM.Model, runtimeCfg.LLM.Timeout,
		runtimeCfg.LLM.MaxExamples, store)
	if err != nil {
		return err
	}
	detector := dedup.New(store, runtimeCfg.Pipeline.Retention, runtimeCfg.Pipeline.TextDistance,
		runtimeCfg.Pipeline.ImageDistance)
	processor := pipeline.New(store, detector, llm, botClient, userClient, pipeline.Options{
		PollInterval: runtimeCfg.Pipeline.PollInterval, RetryBase: runtimeCfg.Pipeline.RetryBase,
		Retention: runtimeCfg.Pipeline.Retention, AlbumSettleDelay: runtimeCfg.Pipeline.AlbumSettleDelay,
		AdThreshold: runtimeCfg.LLM.AdThreshold, ReviewThreshold: runtimeCfg.LLM.ReviewThreshold,
	}, logger)
	callbacks := bot.NewCallbackProcessor(botClient, store, runtimeCfg.Telegram.FeedbackUserIDs, logger)

	go func() { errorsCh <- userClient.Run(ctx) }()
	go func() { errorsCh <- processor.Run(ctx) }()
	go func() { errorsCh <- callbacks.Run(ctx) }()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case runErr := <-errorsCh:
		return runErr
	case <-restartRequested:
		return errors.New("settings changed; restarting service")
	}
}

func loadRuntimeConfig(ctx context.Context, cfg config.Config, store *storage.Store) (config.Config, error) {
	appID, err := strconv.Atoi(setting(ctx, store, "telegram.app_id", strconv.Itoa(cfg.Telegram.AppID)))
	if err != nil {
		return cfg, fmt.Errorf("invalid stored Telegram app_id: %w", err)
	}
	cfg.Telegram.AppID = appID
	cfg.Telegram.AppHash = setting(ctx, store, "telegram.app_hash", cfg.Telegram.AppHash)
	cfg.Telegram.Phone = setting(ctx, store, "telegram.phone", cfg.Telegram.Phone)
	cfg.Telegram.BotToken = setting(ctx, store, "telegram.bot_token", cfg.Telegram.BotToken)
	cfg.Telegram.TargetChannel = setting(ctx, store, "telegram.target_channel", cfg.Telegram.TargetChannel)
	cfg.Telegram.FeedbackUserIDs = parseIDs(setting(ctx, store, "telegram.feedback_user_ids", ""))

	return cfg, nil
}

func setting(ctx context.Context, store *storage.Store, key, fallback string) string {
	value, err := store.GetSetting(ctx, key)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			slog.Warn("load setting", "key", key, "error", err)
		}
		return fallback
	}
	return value
}

func parseIDs(value string) []int64 {
	var result []int64
	for item := range strings.SplitSeq(value, ",") {
		if id, err := strconv.ParseInt(strings.TrimSpace(item), 10, 64); err == nil && id > 0 {
			result = append(result, id)
		}
	}
	return result
}

type resolverProxy struct {
	target web.SourceResolver
	ready  chan struct{}
	mu     sync.RWMutex
	once   sync.Once
}

func newResolverProxy() *resolverProxy { return &resolverProxy{ready: make(chan struct{})} }

func (p *resolverProxy) Set(target web.SourceResolver) {
	p.mu.Lock()
	p.target = target
	p.mu.Unlock()
	p.once.Do(func() { close(p.ready) })
}

func (p *resolverProxy) ResolveAndJoin(ctx context.Context, sourceID int64, raw string) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-p.ready:
	}

	p.mu.RLock()
	target := p.target
	p.mu.RUnlock()
	return target.ResolveAndJoin(ctx, sourceID, raw)
}

func newLogger(cfg config.LogConfig) *slog.Logger {
	level := slog.LevelInfo

	switch strings.ToLower(cfg.Level) {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}
	options := &slog.HandlerOptions{Level: level}
	if cfg.JSON {
		return slog.New(slog.NewJSONHandler(os.Stdout, options))
	}
	return slog.New(slog.NewTextHandler(os.Stdout, options))
}

func hashPassword(args []string) {
	flags := flag.NewFlagSet("hash-password", flag.ExitOnError)
	cost := flags.Int("cost", 12, "bcrypt cost")
	_ = flags.Parse(args)
	if flags.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: telegram-news-reader hash-password [--cost 12] PASSWORD")
		os.Exit(2)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(flags.Arg(0)), *cost)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	_, _ = fmt.Fprintln(os.Stdout, string(hash))
}
