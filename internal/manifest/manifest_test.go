package manifest

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"strings"
	"testing"
)

func validManifest(key string) string {
	return `name: Review Bot
id: com.example.review-bot
version: 1.2.0
homepage: https://example.com/review-bot
callback_url: https://example.com/review-bot/callback
permissions:
  repositories: read
  merge_requests: write
events:
  - merge_request.opened
public_key: ` + key + "\n"
}

func TestParseValidManifest(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	m, err := Parse([]byte(validManifest(hex.EncodeToString(pub))))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if m.ID != "com.example.review-bot" || m.Permissions["merge_requests"] != "write" {
		t.Fatalf("unexpected manifest: %+v", m)
	}
}

func TestParseRejectsInvalidManifests(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	key := hex.EncodeToString(pub)

	for name, body := range map[string]string{
		"bad id":         strings.Replace(validManifest(key), "com.example.review-bot", "Review Bot", 1),
		"http homepage":  strings.Replace(validManifest(key), "https://example.com/review-bot", "http://example.com", 1),
		"bad permission": strings.Replace(validManifest(key), "repositories: read", "repositories: admin", 1),
		"short key":      strings.Replace(validManifest(key), key, key[:32], 1),
		"no events":      strings.Replace(validManifest(key), "events:\n  - merge_request.opened\n", "", 1),
	} {
		if _, err := Parse([]byte(body)); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

func TestSignIsDeterministicForBody(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	first := Sign(priv, []byte("payload"))
	if first != Sign(priv, []byte("payload")) {
		t.Fatal("Ed25519 signatures must be deterministic")
	}
	if first == Sign(priv, []byte("other")) {
		t.Fatal("different bodies must sign differently")
	}
	if len(Digest([]byte("payload"))) != 64 {
		t.Fatal("digest must be a 32-byte hex string")
	}
}
