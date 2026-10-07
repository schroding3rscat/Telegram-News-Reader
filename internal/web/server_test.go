package web

import (
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"github.com/schroding3rscat/Telegram-News-Reader/internal/config"
	"github.com/schroding3rscat/Telegram-News-Reader/internal/storage"
)

func testServer(t *testing.T) *Server {
	t.Helper()
	store, err := storage.Open(filepath.Join(t.TempDir(), "web.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	password, _ := bcrypt.GenerateFromPassword([]byte("secret"), bcrypt.MinCost)
	cfg := config.Default()
	cfg.Admin.Username = "admin"
	cfg.Admin.Password = string(password)
	cfg.Admin.QueryToken = strings.Repeat("t", 24)
	server, err := New(Dependencies{Config: cfg, Store: store})
	if err != nil {
		t.Fatal(err)
	}
	return server
}

func readBody(t *testing.T, response *http.Response) string {
	t.Helper()
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func TestTokenMiddlewareRunsBeforeBasicAuth(t *testing.T) {
	t.Parallel()
	server := testServer(t)
	response, err := server.app.Test(httptest.NewRequest(http.MethodGet, "/setup", http.NoBody))
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("without token: expected 404, got %d", response.StatusCode)
	}

	request := httptest.NewRequest(http.MethodGet, "/setup?token="+strings.Repeat("t", 24), http.NoBody)
	response, err = server.app.Test(request)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("without auth: expected 401, got %d", response.StatusCode)
	}
}

func TestDashboardRequiresSetupAndShowsCharts(t *testing.T) {
	t.Parallel()
	server := testServer(t)
	token := strings.Repeat("t", 24)
	auth := "Basic " + base64.StdEncoding.EncodeToString([]byte("admin:secret"))

	request := httptest.NewRequest(http.MethodGet, "/?token="+token, http.NoBody)
	request.Header.Set("Authorization", auth)
	response, err := server.app.Test(request)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("expected redirect to setup, got %d", response.StatusCode)
	}

	if err := server.store.PutSetting(t.Context(), "setup.complete", "true"); err != nil {
		t.Fatal(err)
	}
	if err := server.store.AddHourlyMetrics(t.Context(), 4, 2, 250, 1); err != nil {
		t.Fatal(err)
	}
	request = httptest.NewRequest(http.MethodGet, "/?token="+token, http.NoBody)
	request.Header.Set("Authorization", auth)
	response, err = server.app.Test(request)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", response.StatusCode)
	}
	body := readBody(t, response)
	for _, snippet := range []string{"Обработано новостей", ">4<", "Отсеяно рекламы", "Время ответа модели"} {
		if !strings.Contains(body, snippet) {
			t.Fatalf("dashboard missing %q in %s", snippet, body)
		}
	}
}

func TestAuthenticatedSetupPage(t *testing.T) {
	t.Parallel()
	server := testServer(t)
	request := httptest.NewRequest(http.MethodGet, "/setup?token="+strings.Repeat("t", 24), http.NoBody)
	request.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("admin:secret")))
	response, err := server.app.Test(request)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", response.StatusCode)
	}
	if response.Header.Get("Set-Cookie") == "" {
		t.Fatal("expected CSRF cookie")
	}
}
