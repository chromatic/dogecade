// Package auth implements OIDC sign-in (Google or any generic issuer) and
// the signed session cookie that carries a customer's identity between
// requests. See session.go for the cookie format, provider.go for the OIDC
// glue, and handlers.go for the login/callback/logout HTTP handlers and the
// RequireAuth/RequireAdmin middleware.
package auth
