package user

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/tg"

	"github.com/schroding3rscat/Telegram-News-Reader/internal/storage"
)

type StoreAuthenticator struct {
	store *storage.Store
}

func NewStoreAuthenticator(store *storage.Store) *StoreAuthenticator {
	return &StoreAuthenticator{store: store}
}

func (a *StoreAuthenticator) Phone(ctx context.Context) (string, error) {
	return a.waitSetting(ctx, "telegram.phone", false)
}

func (a *StoreAuthenticator) Password(ctx context.Context) (string, error) {
	value, err := a.store.GetSetting(ctx, "telegram.password")
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return value, err
}

func (a *StoreAuthenticator) Code(ctx context.Context, _ *tg.AuthSentCode) (string, error) {
	return a.waitSetting(ctx, "telegram.login_code", true)
}

func (a *StoreAuthenticator) AcceptTermsOfService(context.Context, tg.HelpTermsOfService) error {
	return errors.New("automatic Telegram sign-up is disabled; use an existing account")
}

func (a *StoreAuthenticator) SignUp(context.Context) (auth.UserInfo, error) {
	return auth.UserInfo{}, errors.New("automatic Telegram sign-up is disabled")
}

func (a *StoreAuthenticator) waitSetting(ctx context.Context, key string, consume bool) (string, error) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	for {
		value, err := a.store.GetSetting(ctx, key)
		if err == nil && value != "" {
			if consume {
				_ = a.store.PutSetting(ctx, key, "")
			}
			return value, nil
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return "", err
		}

		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-ticker.C:
		}
	}
}
