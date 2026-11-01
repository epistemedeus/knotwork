// Package manifest validates the declarative app manifest used by third-party
// extensions (docs/SRS.md section 8).
package manifest

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

type Manifest struct {
	Name        string            `yaml:"name" json:"name"`
	ID          string            `yaml:"id" json:"id"`
	Version     string            `yaml:"version" json:"version"`
	Homepage    string            `yaml:"homepage" json:"homepage"`
	CallbackURL string            `yaml:"callback_url" json:"callback_url"`
	Permissions map[string]string `yaml:"permissions" json:"permissions"`
	Events      []string          `yaml:"events" json:"events"`
	PublicKey   string            `yaml:"public_key" json:"public_key"`
}

var idPattern = regexp.MustCompile(`^[a-z0-9]+(\.[a-z0-9-]+)+$`)

func Parse(body []byte) (Manifest, error) {
	var m Manifest
	if err := yaml.Unmarshal(body, &m); err != nil {
		return Manifest{}, err
	}
	if err := m.Validate(); err != nil {
		return Manifest{}, err
	}
	return m, nil
}

func (m Manifest) Validate() error {
	if strings.TrimSpace(m.Name) == "" {
		return fmt.Errorf("name is required")
	}
	if !idPattern.MatchString(m.ID) {
		return fmt.Errorf("id must be a reverse-DNS identifier")
	}
	if m.Version == "" {
		return fmt.Errorf("version is required")
	}
	for field, value := range map[string]string{"homepage": m.Homepage, "callback_url": m.CallbackURL} {
		u, err := url.Parse(value)
		if err != nil || u.Scheme != "https" || u.Host == "" {
			return fmt.Errorf("%s must be an https URL", field)
		}
	}
	if len(m.Permissions) == 0 {
		return fmt.Errorf("at least one permission is required")
	}
	for scope, level := range m.Permissions {
		switch level {
		case "read", "write":
		default:
			return fmt.Errorf("permission %q must be read or write", scope)
		}
	}
	if len(m.Events) == 0 {
		return fmt.Errorf("at least one event is required")
	}
	key, err := hex.DecodeString(m.PublicKey)
	if err != nil || len(key) != ed25519.PublicKeySize {
		return fmt.Errorf("public_key must be a 32-byte Ed25519 key in hex")
	}
	return nil
}

// Sign returns the hex Ed25519 signature used in X-Knotwork-Signature webhooks.
func Sign(key ed25519.PrivateKey, body []byte) string {
	return hex.EncodeToString(ed25519.Sign(key, body))
}

func Digest(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}
