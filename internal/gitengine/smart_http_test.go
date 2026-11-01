package gitengine

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"knotwork/internal/domain"
)

func TestSmartHTTPUnauthorizedHeaders(t *testing.T) {
	h := &HTTPHandler{
		RepoByPath: func(ctx context.Context, path string) (domain.Repository, error) {
			return domain.Repository{
				Path:      path,
				IsPrivate: false,
			}, nil
		},
	}

	// Write request (git-receive-pack) requires auth even on public repos
	req := newRequest("/alice/demo.git/info/refs?service=git-receive-pack")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
	if got := rec.Header().Get("WWW-Authenticate"); got != `Basic realm="Knotwork"` {
		t.Fatalf("first WWW-Authenticate = %q, want %q", got, `Basic realm="Knotwork"`)
	}
	vals := rec.Header().Values("WWW-Authenticate")
	if len(vals) < 2 || vals[0] != `Basic realm="Knotwork"` || vals[1] != `Bearer realm="Knotwork", device_flow="/auth/device"` {
		t.Fatalf("WWW-Authenticate values = %v", vals)
	}
}

func TestSmartHTTPPrivateRepoAuth(t *testing.T) {
	h := &HTTPHandler{
		RepoByPath: func(ctx context.Context, path string) (domain.Repository, error) {
			return domain.Repository{
				Path:      path,
				IsPrivate: true,
			}, nil
		},
	}

	// Read request (git-upload-pack) on private repo requires auth
	req := newRequest("/alice/demo.git/info/refs?service=git-upload-pack")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
	if got := rec.Header().Get("WWW-Authenticate"); got != `Basic realm="Knotwork"` {
		t.Fatalf("WWW-Authenticate = %q, want %q", got, `Basic realm="Knotwork"`)
	}
}

func TestSmartHTTPWriteForbidden(t *testing.T) {
	alice := domain.User{ID: "user-1", Username: "alice", Role: domain.RoleMember, IsActive: true}
	h := &HTTPHandler{
		RepoByPath: func(ctx context.Context, path string) (domain.Repository, error) {
			return domain.Repository{
				Path:      path,
				OwnerID:   "user-2", // someone else
				IsPrivate: false,
			}, nil
		},
		UserFromRequest: func(r *http.Request) (domain.User, bool) {
			return alice, true
		},
		Role: func(ctx context.Context, repo domain.Repository, user domain.User) (string, error) {
			return "read", nil // alice only has read
		},
	}

	req := newRequest("/bob/demo.git/info/refs?service=git-receive-pack")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}
}

func TestSmartHTTPNilRepoByPath(t *testing.T) {
	h := &HTTPHandler{}
	req := newRequest("/alice/demo.git/info/refs?service=git-upload-pack")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}
