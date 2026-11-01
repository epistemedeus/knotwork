package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"knotwork/internal/config"
	"knotwork/internal/database"
)

func TestUserFromRequestBasicAuth(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, "sqlite::memory:")
	if err != nil {
		t.Fatalf("database.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	user, err := db.UpsertOIDCUser(ctx, "sub-1", "alice@example.com", "alice", "Alice")
	if err != nil {
		t.Fatalf("UpsertOIDCUser: %v", err)
	}

	key := newTestKey(t)
	tokens := NewTokens(key, "http://localhost:8080", time.Hour)
	svc := &Service{
		DB:     db,
		Cfg:    config.Config{BaseURL: "http://localhost:8080"},
		Tokens: tokens,
		Key:    key,
	}

	jwtToken, err := tokens.Issue(user.ID, user.Username, user.Role, []string{"git:write", "api"})
	if err != nil {
		t.Fatalf("Issue token: %v", err)
	}

	tests := []struct {
		name     string
		setupReq func(req *http.Request)
		wantAuth bool
		wantUser string
	}{
		{
			name: "Bearer token",
			setupReq: func(req *http.Request) {
				req.Header.Set("Authorization", "Bearer "+jwtToken)
			},
			wantAuth: true,
			wantUser: "alice",
		},
		{
			name: "Basic auth knotwork:token (device flow default)",
			setupReq: func(req *http.Request) {
				req.SetBasicAuth("knotwork", jwtToken)
			},
			wantAuth: true,
			wantUser: "alice",
		},
		{
			name: "Basic auth username:token",
			setupReq: func(req *http.Request) {
				req.SetBasicAuth("alice", jwtToken)
			},
			wantAuth: true,
			wantUser: "alice",
		},
		{
			name: "Basic auth token as username",
			setupReq: func(req *http.Request) {
				req.SetBasicAuth(jwtToken, "")
			},
			wantAuth: true,
			wantUser: "alice",
		},
		{
			name: "Basic auth empty username, token as password",
			setupReq: func(req *http.Request) {
				req.SetBasicAuth("", jwtToken)
			},
			wantAuth: true,
			wantUser: "alice",
		},
		{
			name: "Basic auth invalid token",
			setupReq: func(req *http.Request) {
				req.SetBasicAuth("knotwork", "invalid-token")
			},
			wantAuth: false,
		},
		{
			name:     "No auth header",
			setupReq: func(req *http.Request) {},
			wantAuth: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequestWithContext(ctx, "GET", "/", http.NoBody)
			tc.setupReq(req)
			u, ok := svc.UserFromRequest(req)
			if ok != tc.wantAuth {
				t.Fatalf("UserFromRequest authed = %v, want %v", ok, tc.wantAuth)
			}
			if tc.wantAuth && u.Username != tc.wantUser {
				t.Fatalf("username = %q, want %q", u.Username, tc.wantUser)
			}
		})
	}
}
