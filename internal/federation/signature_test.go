package federation

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"net/http"
	"strings"
	"testing"
	"time"
)

func testKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return pub, priv
}

func TestSignVerifyRoundTrip(t *testing.T) {
	pub, priv := testKey(t)
	body := []byte(`{"type":"Offer"}`)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "https://peer.example/inbox", strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	if err := Sign(req, body, priv, "https://knot.example/actor#main-key"); err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if err := Verify(req, body, pub); err != nil {
		t.Fatalf("Verify: %v", err)
	}
}

func TestVerifyRejectsTamperedBody(t *testing.T) {
	pub, priv := testKey(t)
	body := []byte(`{"type":"Offer"}`)
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, "https://peer.example/inbox", http.NoBody)
	if err := Sign(req, body, priv, "key"); err != nil {
		t.Fatal(err)
	}
	if err := Verify(req, []byte(`{"type":"Reject"}`), pub); err == nil {
		t.Fatal("expected digest mismatch")
	}
}

func TestVerifyRejectsStaleDate(t *testing.T) {
	pub, priv := testKey(t)
	body := []byte(`{}`)
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, "https://peer.example/inbox", http.NoBody)
	if err := Sign(req, body, priv, "key"); err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Date", time.Now().Add(-time.Hour).UTC().Format(http.TimeFormat))
	if err := Verify(req, body, pub); err == nil || !strings.Contains(err.Error(), "skew") {
		t.Fatalf("err = %v, want skew rejection", err)
	}
}

func TestPublicKeyPEMRoundTrip(t *testing.T) {
	pub, _ := testKey(t)
	encoded := string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pub}))
	parsed, err := PublicKeyFromPEM(encoded)
	if err != nil {
		t.Fatalf("PublicKeyFromPEM: %v", err)
	}
	if !parsed.Equal(pub) {
		t.Fatal("parsed key differs")
	}
	if _, err := PublicKeyFromPEM("not a key"); err == nil {
		t.Fatal("expected invalid PEM error")
	}
}

func TestDeliverRejectsPlainHTTP(t *testing.T) {
	_, priv := testKey(t)
	err := Deliver(t.Context(), http.DefaultClient, "http://peer.example/inbox", []byte(`{}`), priv, "key")
	if err == nil || !strings.Contains(err.Error(), "https") {
		t.Fatalf("err = %v, want https rejection", err)
	}
}
