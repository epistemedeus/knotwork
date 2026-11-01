package auth

import (
	"context"
	"fmt"

	"knotwork/internal/config"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

type OIDC struct {
	provider *oidc.Provider
	verifier *oidc.IDTokenVerifier
	oauth    *oauth2.Config
}

func NewOIDC(ctx context.Context, cfg config.OIDC) (*OIDC, error) {
	provider, err := oidc.NewProvider(ctx, cfg.Issuer)
	if err != nil {
		return nil, err
	}
	return &OIDC{
		provider: provider,
		verifier: provider.Verifier(&oidc.Config{ClientID: cfg.ClientID}),
		oauth: &oauth2.Config{
			ClientID:     cfg.ClientID,
			ClientSecret: cfg.ClientSecret,
			Endpoint:     provider.Endpoint(),
			RedirectURL:  cfg.RedirectURL,
			Scopes:       []string{oidc.ScopeOpenID, "profile", "email"},
		},
	}, nil
}

func (o *OIDC) AuthCodeURL(state, nonce, verifier string) string {
	return o.oauth.AuthCodeURL(state,
		oidc.Nonce(nonce),
		oauth2.S256ChallengeOption(verifier),
		oauth2.AccessTypeOnline)
}

func (o *OIDC) Exchange(ctx context.Context, code, verifier, nonce string) (oidcClaims, error) {
	token, err := o.oauth.Exchange(ctx, code, oauth2.VerifierOption(verifier))
	if err != nil {
		return oidcClaims{}, fmt.Errorf("token exchange: %w", err)
	}
	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok || rawIDToken == "" {
		return oidcClaims{}, fmt.Errorf("token response did not include an id_token")
	}
	idToken, err := o.verifier.Verify(ctx, rawIDToken)
	if err != nil {
		return oidcClaims{}, fmt.Errorf("verify id_token: %w", err)
	}
	if idToken.Nonce != nonce {
		return oidcClaims{}, fmt.Errorf("nonce mismatch")
	}
	var claims oidcClaims
	if err := idToken.Claims(&claims); err != nil {
		return oidcClaims{}, err
	}
	if claims.Subject == "" {
		claims.Subject = idToken.Subject
	}
	return claims, nil
}
