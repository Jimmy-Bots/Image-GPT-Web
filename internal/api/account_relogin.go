package api

import (
	"context"
	"fmt"
	"strings"
	"time"

	"gpt-image-web/internal/register"
)

type accountReloginResult struct {
	OldToken string
	NewToken string
	Email    string
}

func (u *ChatGPTUpstream) reloginAccount(ctx context.Context, accessToken string) (accountReloginResult, error) {
	account, err := u.store.GetAccount(ctx, accessToken)
	if err != nil {
		return accountReloginResult{}, err
	}
	email := strings.TrimSpace(account.Email)
	password := strings.TrimSpace(account.Password)
	if email == "" || password == "" {
		return accountReloginResult{}, fmt.Errorf("missing email or password for relogin")
	}
	settings, _ := u.store.GetSettings(ctx)
	cfg := register.Config{
		ProxyURL:             fallbackString(strings.TrimSpace(stringMapValue(mapAnyValue(settings["register"]), "proxy")), ""),
		RequestTimeout:       30 * time.Second,
		SentinelTimeout:      20 * time.Second,
		TokenExchangeTimeout: 60 * time.Second,
		WaitTimeout:          30 * time.Second,
		WaitInterval:         2 * time.Second,
	}
	if strings.TrimSpace(cfg.ProxyURL) == "" {
		cfg.ProxyURL = ""
	}
	loginOnly, err := register.NewLoginOnly(cfg)
	if err != nil {
		return accountReloginResult{}, err
	}
	tokens, err := loginOnly.LoginAndExchangeTokens(ctx, email, password)
	if err != nil {
		return accountReloginResult{}, err
	}
	newToken := strings.TrimSpace(tokens.AccessToken)
	if newToken == "" {
		return accountReloginResult{}, fmt.Errorf("empty access token after relogin")
	}
	if _, err := u.store.UpsertAccountToken(ctx, newToken, password); err != nil {
		return accountReloginResult{}, err
	}
	if _, err := u.store.ReplaceAccountToken(ctx, accessToken, newToken); err != nil {
		return accountReloginResult{}, err
	}
	return accountReloginResult{
		OldToken: accessToken,
		NewToken: newToken,
		Email:    email,
	}, nil
}
