package auth

import (
	"crypto/ed25519"
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

type Claims struct {
	UserID   string   `json:"uid"`
	Username string   `json:"username"`
	Role     string   `json:"role"`
	Scopes   []string `json:"scope"`
	jwt.RegisteredClaims
}

type Tokens struct {
	key    ed25519.PrivateKey
	issuer string
	ttl    time.Duration
}

func NewTokens(key ed25519.PrivateKey, issuer string, ttl time.Duration) *Tokens {
	return &Tokens{key: key, issuer: issuer, ttl: ttl}
}

func (t *Tokens) TTL() time.Duration { return t.ttl }

func (t *Tokens) Issue(userID, username, role string, scopes []string) (string, error) {
	now := time.Now()
	claims := Claims{
		UserID:   userID,
		Username: username,
		Role:     role,
		Scopes:   scopes,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    t.issuer,
			Subject:   userID,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(t.ttl)),
		},
	}
	return jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims).SignedString(t.key)
}

func (t *Tokens) Parse(raw string) (*Claims, error) {
	var claims Claims
	token, err := jwt.ParseWithClaims(raw, &claims, func(token *jwt.Token) (any, error) {
		if token.Method != jwt.SigningMethodEdDSA {
			return nil, fmt.Errorf("unexpected signing method %v", token.Header["alg"])
		}
		return t.key.Public(), nil
	}, jwt.WithIssuer(t.issuer), jwt.WithValidMethods([]string{"EdDSA"}))
	if err != nil {
		return nil, err
	}
	if !token.Valid {
		return nil, errors.New("invalid token")
	}
	return &claims, nil
}
