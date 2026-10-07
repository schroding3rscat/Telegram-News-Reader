package web

import (
	"encoding/base64"
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
