// Package oauth2 adapts golang.org/x/oauth2 grants to httpx token authentication.
// Sign-in flows remain in x/oauth2 and the application.
package oauth2

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/ashtonian/pkg/httpx"
	xoauth2 "golang.org/x/oauth2"
)

// Source refreshes cfg's grant even when a 401 rejects a token before expiry.
// It owns copies of cfg and tok. onRotate runs before returning a new token;
// persistence failure is returned, and the rotated token remains in memory.
// Set xoauth2.HTTPClient on request contexts to supply the token endpoint's
// httpx client. It must not use this token source, which would recurse.
func Source(cfg *xoauth2.Config, tok *xoauth2.Token, onRotate func(context.Context, *xoauth2.Token) error, opts ...httpx.TokenOption) (*httpx.RefreshingTokenSource, error) {
	if cfg == nil || tok == nil || tok.RefreshToken == "" || !bearer(tok) {
		return nil, fmt.Errorf("%w: oauth2 source requires config and bearer refresh token", httpx.ErrInvalidConfig)
	}
	// Config contains an internal synchronization cache and must not be copied
	// after use; copy public configuration into a fresh value instead.
	config := &xoauth2.Config{ClientID: cfg.ClientID, ClientSecret: cfg.ClientSecret, Endpoint: cfg.Endpoint, RedirectURL: cfg.RedirectURL, Scopes: slices.Clone(cfg.Scopes)}
	current := *tok
	return httpx.NewTokenSource(current.AccessToken, current.Expiry, func(ctx context.Context) (string, time.Time, error) {
		// An access-token-free seed forces the grant, bypassing x/oauth2's
		// still-valid cached access token on a 401.
		next, err := config.TokenSource(ctx, &xoauth2.Token{RefreshToken: current.RefreshToken}).Token()
		if err != nil {
			return "", time.Time{}, err
		}
		if !bearer(next) {
			return "", time.Time{}, fmt.Errorf("%w: oauth2 token type is not bearer", httpx.ErrAuthRefresh)
		}
		current = *next
		if onRotate != nil {
			snapshot := current
			if err := onRotate(ctx, &snapshot); err != nil {
				return current.AccessToken, current.Expiry, err
			}
		}
		return current.AccessToken, current.Expiry, nil
	}, opts...)
}

// Reacquire obtains a fresh grant on expiry or rejection, for grants without a
// refresh token such as client credentials. newSource must return a fresh source
// bound to ctx, not a cached source retaining an earlier context.
func Reacquire(newSource func(context.Context) xoauth2.TokenSource, opts ...httpx.TokenOption) (*httpx.RefreshingTokenSource, error) {
	if newSource == nil {
		return nil, fmt.Errorf("%w: oauth2 grant factory is nil", httpx.ErrInvalidConfig)
	}
	return httpx.NewTokenSource("", time.Time{}, func(ctx context.Context) (string, time.Time, error) {
		source := newSource(ctx)
		if source == nil {
			return "", time.Time{}, fmt.Errorf("%w: oauth2 grant factory returned nil", httpx.ErrAuthRefresh)
		}
		token, err := source.Token()
		if err != nil {
			return "", time.Time{}, err
		}
		if token == nil || !bearer(token) {
			return "", time.Time{}, fmt.Errorf("%w: oauth2 grant returned no bearer token", httpx.ErrAuthRefresh)
		}
		return token.AccessToken, token.Expiry, nil
	}, opts...)
}

func bearer(token *xoauth2.Token) bool {
	return token.TokenType == "" || strings.EqualFold(token.TokenType, "Bearer")
}
