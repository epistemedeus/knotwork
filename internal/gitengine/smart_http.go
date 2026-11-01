package gitengine

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/cgi"
	"strings"

	"knotwork/internal/domain"
)

// HTTPHandler serves Git Smart HTTP by proxying to `git http-backend` through
// the standard library CGI bridge. Request and response bodies are streamed,
// so pushes never land in server memory or temporary files.
type HTTPHandler struct {
	Manager         *Manager
	RepoByPath      func(ctx context.Context, path string) (domain.Repository, error)
	UserFromRequest func(r *http.Request) (domain.User, bool)
	Role            func(ctx context.Context, repo domain.Repository, user domain.User) (string, error)
	Logger          *slog.Logger
}

// Register installs catch-all GET/POST handlers. Git clone URLs live at the
// site root (`/{owner}/{repo}.git`), which cannot be expressed as a ServeMux
// wildcard without colliding with other two-segment subtrees.
func (h *HTTPHandler) Register(mux *http.ServeMux) {
	mux.Handle("GET /", h)
	mux.Handle("POST /", h)
}

type gitRequest struct {
	owner    string
	repo     string
	endpoint string
	write    bool
}

func parseGitRequest(r *http.Request) (gitRequest, bool) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 3 || parts[0] == "" || parts[1] == "" || !strings.HasSuffix(parts[1], ".git") {
		return gitRequest{}, false
	}
	owner := parts[0]
	repo := strings.TrimSuffix(parts[1], ".git")
	if repo == "" {
		return gitRequest{}, false
	}
	switch {
	case len(parts) == 4 && parts[2] == "info" && parts[3] == "refs":
		return gitRequest{
			owner:    owner,
			repo:     repo,
			endpoint: "info/refs",
			write:    r.URL.Query().Get("service") == "git-receive-pack",
		}, true
	case len(parts) == 3 && parts[2] == "git-upload-pack":
		return gitRequest{owner: owner, repo: repo, endpoint: "git-upload-pack"}, true
	case len(parts) == 3 && parts[2] == "git-receive-pack":
		return gitRequest{owner: owner, repo: repo, endpoint: "git-receive-pack", write: true}, true
	}
	return gitRequest{}, false
}

func (h *HTTPHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	req, ok := parseGitRequest(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	h.serve(w, r, req)
}

func (h *HTTPHandler) serve(w http.ResponseWriter, r *http.Request, req gitRequest) {
	if h.RepoByPath == nil {
		http.NotFound(w, r)
		return
	}
	repoPath := req.owner + "/" + req.repo

	repo, err := h.RepoByPath(r.Context(), repoPath)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return
		}
		http.NotFound(w, r)
		return
	}

	var user domain.User
	var authed bool
	if h.UserFromRequest != nil {
		user, authed = h.UserFromRequest(r)
	}

	if req.write {
		if !authed {
			unauthorized(w)
			return
		}
		if !h.canWrite(r.Context(), repo, user, w) {
			return
		}
	} else if repo.IsPrivate {
		if !authed {
			unauthorized(w)
			return
		}
		if !h.canRead(r.Context(), repo, user, w) {
			return
		}
	}
	// Public reads may still carry an identity; the hook server uses it for
	// audit trails even though no authorization is required.

	env := []string{
		"GIT_PROJECT_ROOT=" + h.Manager.Root,
		"GIT_HTTP_EXPORT_ALL=1",
		"PATH_INFO=" + r.URL.Path,
		"REMOTE_USER=" + user.Username,
		"KNOTWORK_ACTOR_ID=" + user.ID,
		"KNOTWORK_REPO_PATH=" + repo.DiskPath,
		"KNOTWORK_SOCKET=" + h.Manager.SocketPath,
	}
	handler := &cgi.Handler{
		Path:       h.Manager.GitBinary,
		Args:       []string{"http-backend"},
		Env:        env,
		InheritEnv: []string{"PATH", "HOME", "LANG", "TZ"},
	}
	handler.ServeHTTP(w, r)
}

func (h *HTTPHandler) canWrite(ctx context.Context, repo domain.Repository, user domain.User, w http.ResponseWriter) bool {
	role, err := h.Role(ctx, repo, user)
	if err != nil {
		if h.Logger != nil {
			h.Logger.Error("repo role lookup failed", "repo", repo.Path, "error", err)
		}
		http.Error(w, "internal error", http.StatusInternalServerError)
		return false
	}
	if role != "admin" && role != "write" {
		http.Error(w, "write access denied", http.StatusForbidden)
		return false
	}
	return true
}

func (h *HTTPHandler) canRead(ctx context.Context, repo domain.Repository, user domain.User, w http.ResponseWriter) bool {
	role, err := h.Role(ctx, repo, user)
	if err != nil || role == "" {
		http.Error(w, "read access denied", http.StatusForbidden)
		return false
	}
	return true
}

func unauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Basic realm="Knotwork"`)
	w.Header().Add("WWW-Authenticate", `Bearer realm="Knotwork", device_flow="/auth/device"`)
	http.Error(w, "authentication required", http.StatusUnauthorized)
}
