package auth

import (
	"context"
	"fmt"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// IDClaims is the subset of an OIDC ID token's claims used to identify and
// display a signed-in customer.
type IDClaims struct {
	Issuer  string
	Subject string
	Email   string
	Name    string
}

// exchanger performs the OIDC authorization-code flow: building the
// provider's login URL and exchanging a returned code for verified ID
// token claims. Abstracted so handlers can be tested against a fake without
// a real issuer to talk to (discovery requires a live HTTP round trip).
type exchanger interface {
	AuthCodeURL(state, nonce string) string
	Exchange(ctx context.Context, code, nonce string) (IDClaims, error)
}

// Provider wraps a discovered OIDC issuer (Google or any other generic
// issuer configured via DOGECADE_OIDC_ISSUER_URL) and an OAuth2 client
// registered with it.
type Provider struct {
	oauth2Config oauth2.Config
	verifier     *oidc.IDTokenVerifier
}

// NewProvider performs OIDC discovery against issuerURL (a live HTTP call to
// its /.well-known/openid-configuration) and builds a Provider for the given
// client credentials and callback URL.
func NewProvider(ctx context.Context, issuerURL, clientID, clientSecret, redirectURL string) (*Provider, error) {
	oidcProvider, err := oidc.NewProvider(ctx, issuerURL)
	if err != nil {
		return nil, fmt.Errorf("failed to discover OIDC issuer %q: %w", issuerURL, err)
	}
	return &Provider{
		oauth2Config: oauth2.Config{
			ClientID:     clientID,
			ClientSecret: clientSecret,
			Endpoint:     oidcProvider.Endpoint(),
			RedirectURL:  redirectURL,
			Scopes:       []string{oidc.ScopeOpenID, "email", "profile"},
		},
		verifier: oidcProvider.Verifier(&oidc.Config{ClientID: clientID}),
	}, nil
}

// AuthCodeURL builds the provider login URL for the given anti-CSRF state
// and replay-protection nonce.
func (p *Provider) AuthCodeURL(state, nonce string) string {
	return p.oauth2Config.AuthCodeURL(state, oidc.Nonce(nonce))
}

// Exchange trades an authorization code for a verified ID token, checking
// that its nonce matches the one issued at login time.
func (p *Provider) Exchange(ctx context.Context, code, nonce string) (IDClaims, error) {
	token, err := p.oauth2Config.Exchange(ctx, code)
	if err != nil {
		return IDClaims{}, fmt.Errorf("failed to exchange authorization code: %w", err)
	}

	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok {
		return IDClaims{}, fmt.Errorf("token response did not include an id_token")
	}

	idToken, err := p.verifier.Verify(ctx, rawIDToken)
	if err != nil {
		return IDClaims{}, fmt.Errorf("failed to verify ID token: %w", err)
	}
	if idToken.Nonce != nonce {
		return IDClaims{}, fmt.Errorf("ID token nonce mismatch")
	}

	var extra struct {
		Email string `json:"email"`
		Name  string `json:"name"`
	}
	if err := idToken.Claims(&extra); err != nil {
		return IDClaims{}, fmt.Errorf("failed to parse ID token claims: %w", err)
	}

	return IDClaims{
		Issuer:  idToken.Issuer,
		Subject: idToken.Subject,
		Email:   extra.Email,
		Name:    extra.Name,
	}, nil
}
