package web

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/base64"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gofiber/fiber/v3"
	"golang.org/x/crypto/bcrypt"

	"github.com/schroding3rscat/Telegram-News-Reader/internal/config"
	"github.com/schroding3rscat/Telegram-News-Reader/internal/storage"
)

//go:embed ui/*
var uiFS embed.FS

type SourceResolver interface {
	ResolveAndJoin(ctx context.Context, sourceID int64, raw string) error
}

type Dependencies struct {
	Resolver          SourceResolver
	Store             *storage.Store
	OnConfigured      func()
	OnSettingsChanged func()
	Logger            *slog.Logger
	Config            config.Config
}

type Server struct {
	resolver        SourceResolver
	app             *fiber.App
	store           *storage.Store
	configured      func()
	settingsChanged func()
	logger          *slog.Logger
	templates       *template.Template
	authCache       sync.Map
	css             []byte
	config          config.Config
}

type pageData struct {
	Title           string
	Page            string
	Token           string
	CSRF            string
	Flash           string
	TargetChannel   string
	FeedbackUserIDs string
	BotStatus       string
	LatencyLabel    string
	ProcessedChart  ChartView
	AdsChart        ChartView
	LatencyChart    ChartView
	Sources         []storage.Source
	Quarantine      []storage.QuarantineItem
	Topics          []storage.Topic
	Dashboard       storage.Dashboard
	Days            int
}

const (
	adminBodyLimit       = 2 * 1024 * 1024
	adminReadTimeout     = 10 * time.Second
	adminWriteTimeout    = 20 * time.Second
	authCacheTTL         = 5 * time.Minute
	csrfTokenBytes       = 32
	csrfCookieMaxAge     = 24 * 60 * 60
	sourceResolveTimeout = 2 * time.Minute
	quarantinePageSize   = 100
)

func New(deps Dependencies) (*Server, error) {
	if deps.Store == nil {
		return nil, errors.New("web storage is required")
	}
	templates, err := template.ParseFS(uiFS, "ui/*.html")
	if err != nil {
		return nil, err
	}
	css, err := uiFS.ReadFile("ui/app.css")
	if err != nil {
		return nil, err
	}
	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}
	server := &Server{
		config: deps.Config, store: deps.Store, resolver: deps.Resolver,
		configured: deps.OnConfigured, settingsChanged: deps.OnSettingsChanged,
		logger: deps.Logger, templates: templates, css: css,
	}
	server.app = fiber.New(fiber.Config{
		AppName:      "Telegram News Reader",
		BodyLimit:    adminBodyLimit,
		ReadTimeout:  adminReadTimeout,
		WriteTimeout: adminWriteTimeout,
	})
	server.routes()
	return server, nil
}

func (s *Server) Listen() error { return s.app.Listen(s.config.Listen) }

func (s *Server) Shutdown() error { return s.app.Shutdown() }

func (s *Server) Handler() *fiber.App { return s.app }

func (s *Server) routes() {
	s.app.Use(s.queryTokenMiddleware)
	s.app.Use(s.basicAuthMiddleware)
	s.app.Use(s.securityHeaders)
	s.app.Use(s.csrfMiddleware)

	s.app.Get("/assets/app.css", func(c fiber.Ctx) error {
		c.Type("css", "utf-8")
		return c.Send(s.css)
	})
	s.app.Get("/healthz", func(c fiber.Ctx) error {
		if err := s.store.Ping(c.Context()); err != nil {
			return c.Status(http.StatusServiceUnavailable).SendString("unhealthy")
		}
		return c.SendString("ok")
	})
	s.app.Get("/", s.root)
	s.app.Get("/setup", s.setupPage)
	s.app.Post("/setup", s.setupSave)
	s.app.Get("/sources", s.sourcesPage)
	s.app.Post("/sources", s.sourceAdd)
	s.app.Post("/sources/:id/delete", s.sourceDelete)
	s.app.Get("/quarantine", s.quarantinePage)
	s.app.Post("/quarantine/:id/false-positive", s.falsePositive)
	s.app.Get("/topics", s.topicsPage)
	s.app.Post("/topics", s.topicAdd)
	s.app.Post("/topics/:id/delete", s.topicDelete)
	s.app.Get("/settings", s.settingsPage)
	s.app.Post("/settings", s.settingsSave)
	s.app.Post("/telegram/code", s.telegramCode)
	s.app.Use(func(c fiber.Ctx) error { return c.SendStatus(http.StatusNotFound) })
}

func (s *Server) queryTokenMiddleware(c fiber.Ctx) error {
	actual := c.Query("token")
	expected := s.config.Admin.QueryToken
	if len(actual) != len(expected) || subtle.ConstantTimeCompare([]byte(actual), []byte(expected)) != 1 {
		return c.SendStatus(http.StatusNotFound)
	}
	return c.Next()
}

func (s *Server) basicAuthMiddleware(c fiber.Ctx) error {
	header := c.Get("Authorization")
	if !strings.HasPrefix(header, "Basic ") {
		c.Set("WWW-Authenticate", `Basic realm="Telegram News Reader", charset="UTF-8"`)
		return c.SendStatus(http.StatusUnauthorized)
	}
	cacheKey := sha256.Sum256([]byte(header))
	if cached, ok := s.authCache.Load(cacheKey); ok {
		expiresAt, valid := cached.(time.Time)
		if valid && time.Now().Before(expiresAt) {
			return c.Next()
		}
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(header, "Basic "))
	if err != nil {
		return c.SendStatus(http.StatusUnauthorized)
	}
	username, password, ok := strings.Cut(string(decoded), ":")
	if !ok || subtle.ConstantTimeCompare([]byte(username), []byte(s.config.Admin.Username)) != 1 ||
		bcrypt.CompareHashAndPassword([]byte(s.config.Admin.Password), []byte(password)) != nil {
		return c.SendStatus(http.StatusUnauthorized)
	}

	s.authCache.Store(cacheKey, time.Now().Add(authCacheTTL))
	return c.Next()
}

func (s *Server) securityHeaders(c fiber.Ctx) error {
	c.Set("X-Content-Type-Options", "nosniff")
	c.Set("X-Frame-Options", "DENY")
	c.Set("Referrer-Policy", "no-referrer")
	c.Set("Content-Security-Policy", "default-src 'self'; style-src 'self'; form-action 'self'; frame-ancestors 'none'")
	c.Set("Cache-Control", "no-store")
	return c.Next()
}

func (s *Server) csrfMiddleware(c fiber.Ctx) error {
	token := c.Cookies("tnr_csrf")
	if token == "" {
		var err error
		token, err = randomToken(csrfTokenBytes)
		if err != nil {
			return c.SendStatus(http.StatusInternalServerError)
		}

		c.Cookie(&fiber.Cookie{
			Name: "tnr_csrf", Value: token, Path: "/", HTTPOnly: true,
			Secure: true, SameSite: "Strict", MaxAge: csrfCookieMaxAge,
		})
	}

	c.Locals("csrf", token)
	if c.Method() == http.MethodPost {
		provided := c.FormValue("csrf")
		if len(provided) != len(token) || subtle.ConstantTimeCompare([]byte(provided), []byte(token)) != 1 {
			return c.Status(http.StatusForbidden).SendString("invalid CSRF token")
		}
	}
	return c.Next()
}

func (s *Server) root(c fiber.Ctx) error {
	if !s.setupComplete(c.Context()) {
		return s.redirect(c, "/setup")
	}
	dashboard, err := s.store.Dashboard(c.Context(), dashboardDays, time.Now())
	if err != nil {
		return err
	}
	latencyLabel := "нет данных"
	if dashboard.LatencyCalls > 0 {
		latencyLabel = fmt.Sprintf("%.0f мс", dashboard.LatencyAvgMs)
	}
	return s.render(c, &pageData{
		Title:          "Обзор",
		Page:           "dashboard",
		Dashboard:      dashboard,
		Days:           dashboardDays,
		LatencyLabel:   latencyLabel,
		ProcessedChart: newChart("Обработанные новости", "", dashboard.Processed),
		AdsChart:       newChart("Отсеянная реклама", "", dashboard.Ads),
		LatencyChart:   newChart("Время ответа модели", "мс", dashboard.Latency),
		Flash:          flash(c),
	})
}

func (s *Server) setupPage(c fiber.Ctx) error {
	if s.setupComplete(c.Context()) {
		return s.redirect(c, "/")
	}
	return s.render(c, &pageData{Title: "Настройка", Page: "setup"})
}

func (s *Server) setupSave(c fiber.Ctx) error {
	appID, err := strconv.Atoi(c.FormValue("app_id"))
	if err != nil || appID <= 0 {
		return c.Status(http.StatusBadRequest).SendString("invalid Telegram API ID")
	}
	values := map[string]string{
		"telegram.app_id":            strconv.Itoa(appID),
		"telegram.app_hash":          strings.TrimSpace(c.FormValue("app_hash")),
		"telegram.phone":             strings.TrimSpace(c.FormValue("phone")),
		"telegram.password":          c.FormValue("telegram_password"),
		"telegram.bot_token":         strings.TrimSpace(c.FormValue("bot_token")),
		"telegram.target_channel":    strings.TrimSpace(c.FormValue("target_channel")),
		"telegram.feedback_user_ids": normalizeIDs(c.FormValue("feedback_user_ids")),
		"setup.complete":             "true",
	}
	for key, value := range values {
		if key != "telegram.password" && value == "" {
			return c.Status(http.StatusBadRequest).SendString("all required fields must be set")
		}
		if err := s.store.PutSetting(c.Context(), key, value); err != nil {
			return err
		}
	}
	if s.configured != nil {
		s.configured()
	}
	return s.redirect(c, "/?ok=configured")
}

func (s *Server) sourcesPage(c fiber.Ctx) error {
	if !s.setupComplete(c.Context()) {
		return s.redirect(c, "/setup")
	}
	sources, err := s.store.ListSources(c.Context())
	if err != nil {
		return err
	}
	return s.render(c, &pageData{Title: "Источники", Page: "sources", Sources: sources, Flash: flash(c)})
}

func (s *Server) sourceAdd(c fiber.Ctx) error {
	raw := strings.TrimSpace(c.FormValue("url"))
	id, err := s.store.AddSource(c.Context(), storage.Source{URL: raw, Enabled: true, Status: "pending"})
	if err != nil {
		return c.Status(http.StatusBadRequest).SendString(err.Error())
	}
	if s.resolver != nil {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), sourceResolveTimeout)
			defer cancel()
			if err := s.resolver.ResolveAndJoin(ctx, id, raw); err != nil {
				s.logger.Warn("resolve source", "url", raw, "error", err)
			}
		}()
	}
	return s.redirect(c, "/sources?ok=source-added")
}

func (s *Server) sourceDelete(c fiber.Ctx) error {
	id, err := strconv.ParseInt(c.Params("id"), 10, 64)
	if err != nil {
		return c.SendStatus(http.StatusBadRequest)
	}
	if err := s.store.DeleteSource(c.Context(), id); err != nil {
		return err
	}
	return s.redirect(c, "/sources?ok=source-deleted")
}

func (s *Server) quarantinePage(c fiber.Ctx) error {
	items, err := s.store.ListQuarantine(c.Context(), quarantinePageSize, 0)
	if err != nil {
		return err
	}
	return s.render(c, &pageData{Title: "Реклама", Page: "quarantine", Quarantine: items, Flash: flash(c)})
}

func (s *Server) falsePositive(c fiber.Ctx) error {
	id, err := strconv.ParseInt(c.Params("id"), 10, 64)
	if err != nil {
		return c.SendStatus(http.StatusBadRequest)
	}
	if err := s.store.ResolveQuarantine(c.Context(), id, "false_positive"); err != nil {
		return err
	}
	return s.redirect(c, "/quarantine?ok=false-positive")
}

func (s *Server) topicsPage(c fiber.Ctx) error {
	topics, err := s.store.ListTopics(c.Context())
	if err != nil {
		return err
	}
	return s.render(c, &pageData{Title: "Темы", Page: "topics", Topics: topics, Flash: flash(c)})
}

func (s *Server) topicAdd(c fiber.Ctx) error {
	if err := s.store.AddTopic(c.Context(), c.FormValue("name")); err != nil {
		return err
	}
	return s.redirect(c, "/topics?ok=topic-added")
}

func (s *Server) topicDelete(c fiber.Ctx) error {
	id, err := strconv.ParseInt(c.Params("id"), 10, 64)
	if err != nil {
		return c.SendStatus(http.StatusBadRequest)
	}
	if err := s.store.DeleteTopic(c.Context(), id); err != nil {
		return err
	}
	return s.redirect(c, "/topics?ok=topic-deleted")
}

func (s *Server) settingsPage(c fiber.Ctx) error {
	target, _ := s.store.GetSetting(c.Context(), "telegram.target_channel")
	ids, _ := s.store.GetSetting(c.Context(), "telegram.feedback_user_ids")
	botStatus, _ := s.store.GetSetting(c.Context(), "status.bot")
	return s.render(c, &pageData{
		Title: "Настройки", Page: "settings", TargetChannel: target,
		FeedbackUserIDs: ids, BotStatus: botStatus, Flash: flash(c),
	})
}

func (s *Server) settingsSave(c fiber.Ctx) error {
	values := map[string]string{
		"telegram.target_channel":    strings.TrimSpace(c.FormValue("target_channel")),
		"telegram.feedback_user_ids": normalizeIDs(c.FormValue("feedback_user_ids")),
	}
	if token := strings.TrimSpace(c.FormValue("bot_token")); token != "" {
		values["telegram.bot_token"] = token
	}
	for key, value := range values {
		if err := s.store.PutSetting(c.Context(), key, value); err != nil {
			return err
		}
	}
	if s.settingsChanged != nil {
		go func() {
			time.Sleep(time.Second)
			s.settingsChanged()
		}()
	}
	return s.redirect(c, "/settings?ok=saved")
}

func (s *Server) telegramCode(c fiber.Ctx) error {
	code := strings.TrimSpace(c.FormValue("code"))
	if code == "" {
		return c.SendStatus(http.StatusBadRequest)
	}
	if err := s.store.PutSetting(c.Context(), "telegram.login_code", code); err != nil {
		return err
	}
	return s.redirect(c, "/settings?ok=code-saved")
}

func (s *Server) render(c fiber.Ctx, data *pageData) error {
	data.Token = s.config.Admin.QueryToken
	data.CSRF, _ = c.Locals("csrf").(string)
	var output strings.Builder
	if err := s.templates.ExecuteTemplate(&output, "index.html", data); err != nil {
		return err
	}

	c.Type("html", "utf-8")
	return c.SendString(output.String())
}

func (s *Server) redirect(c fiber.Ctx, path string) error {
	separator := "?"
	if strings.Contains(path, "?") {
		separator = "&"
	}
	return c.Redirect().Status(http.StatusSeeOther).To(path + separator + "token=" + s.config.Admin.QueryToken)
}

func (s *Server) setupComplete(ctx context.Context) bool {
	value, err := s.store.GetSetting(ctx, "setup.complete")
	return err == nil && value == "true"
}

func randomToken(bytesCount int) (string, error) {
	value := make([]byte, bytesCount)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func normalizeIDs(value string) string {
	var ids []string
	for item := range strings.SplitSeq(value, ",") {
		id, err := strconv.ParseInt(strings.TrimSpace(item), 10, 64)
		if err == nil && id > 0 {
			ids = append(ids, strconv.FormatInt(id, 10))
		}
	}
	return strings.Join(ids, ",")
}

func flash(c fiber.Ctx) string {
	switch c.Query("ok") {
	case "configured":
		return "Настройка сохранена."
	case "source-added":
		return "Источник добавлен и проверяется."
	case "source-deleted":
		return "Источник удалён."
	case "false-positive":
		return "Сообщение сохранено как пример «не реклама»."
	case "topic-added":
		return "Тема добавлена."
	case "topic-deleted":
		return "Тема удалена."
	case "saved":
		return "Настройки сохранены."
	case "code-saved":
		return "Код передан Telegram-клиенту."
	default:
		return ""
	}
}
