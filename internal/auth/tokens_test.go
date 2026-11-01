package auth

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"
)

func newTestKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return priv
}

func TestTokensRoundTrip(t *testing.T) {
	tokens := NewTokens(newTestKey(t), "https://knot.example", time.Hour)
	raw, err := tokens.Issue("user-1", "alice", "admin", []string{"git:write", "api"})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	claims, err := tokens.Parse(raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if claims.UserID != "user-1" || claims.Username != "alice" || claims.Role != "admin" {
		t.Fatalf("claims = %+v", claims)
	}
	if len(claims.Scopes) != 2 || claims.Scopes[0] != "git:write" {
		t.Fatalf("scopes = %v", claims.Scopes)
	}
	if claims.Issuer != "https://knot.example" {
		t.Fatalf("issuer = %q", claims.Issuer)
	}
}

func TestTokensRejectForeignSignature(t *testing.T) {
	issuer := NewTokens(newTestKey(t), "https://knot.example", time.Hour)
	other := NewTokens(newTestKey(t), "https://knot.example", time.Hour)
	raw, err := issuer.Issue("user-1", "alice", "member", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.Parse(raw); err == nil {
		t.Fatal("token verified with the wrong key")
	}
	if _, err := issuer.Parse(raw + "x"); err == nil {
		t.Fatal("tampered token verified")
	}
}

func TestTokensRejectWrongIssuer(t *testing.T) {
	key := newTestKey(t)
	raw, err := NewTokens(key, "https://a.example", time.Hour).Issue("u", "u", "member", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewTokens(key, "https://b.example", time.Hour).Parse(raw); err == nil {
		t.Fatal("token with a foreign issuer verified")
	}
}

func TestSanitizeUsername(t *testing.T) {
	cases := map[string]string{
		"Alice":              "alice",
		" alice@example.com": "aliceexample.com",
		"a b/c":              "abc",
		"--weird..name--":    "weird..name",
		"":                   "",
	}
	for in, want := range cases {
		if got := sanitizeUsername(in); got != want {
			t.Errorf("sanitizeUsername(%q) = %q, want %q", in, got, want)
		}
	}
	if got := sanitizeUsername("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"); len(got) != 39 {
		t.Errorf("long username length = %d, want 39", len(got))
	}
}

func TestSafeNextRejectsOpenRedirects(t *testing.T) {
	for in, want := range map[string]string{
		"":               "/",
		"/repos":         "/repos",
		"//evil.example": "/",
		"https://evil":   "/",
	} {
		if got := safeNext(in); got != want {
			t.Errorf("safeNext(%q) = %q, want %q", in, got, want)
		}
	}
}
