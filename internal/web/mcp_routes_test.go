package web

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"knotwork/internal/auth"
	"knotwork/internal/config"
	"knotwork/internal/database"
	"knotwork/internal/domain"
	"knotwork/internal/gitengine"
	kmcp "knotwork/internal/mcp"
)

func TestMCPThroughWebStack(t *testing.T) {
	ctx := t.Context()
	db, err := database.Open(ctx, "sqlite::memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	user, err := db.UpsertOIDCUser(ctx, "alice", "alice@example.com", "alice", "Alice")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SetUserRole(ctx, user.ID, domain.RoleMember); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Knotwork Test",
			"GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=Knotwork Test",
			"GIT_COMMITTER_EMAIL=test@example.com",
			"GIT_CONFIG_NOSYSTEM=1",
			"GIT_CONFIG_GLOBAL=/dev/null",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "--initial-branch=main")
	if err := os.WriteFile(filepath.Join(dir, "README"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", "README")
	git("commit", "-m", "init")
	shaCmd := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "HEAD")
	shaOut, err := shaCmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	repo, err := db.CreateRepo(ctx, user.ID, "demo", "demo", "alice/demo", dir, true)
	if err != nil {
		t.Fatal(err)
	}
	mr, err := db.CreateFederatedMR(ctx, repo.ID, user.ID, "forge://peer/demo", "feature", "main", "Demo change", "desc", "actor", "peer.example", strings.TrimSpace(string(shaOut)))
	if err != nil {
		t.Fatal(err)
	}
	token, err := db.CreateSession(ctx, user.ID, "api", time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	var listen net.ListenConfig
	ln, err := listen.Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	base := "http://" + ln.Addr().String()
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.DiscardHandler)
	authSvc := &auth.Service{
		DB:     db,
		Cfg:    config.Config{BaseURL: base, DataDir: t.TempDir()},
		Tokens: auth.NewTokens(testWebKey(t), base, time.Hour),
		Log:    log,
	}
	app := &Server{
		Cfg:  authSvc.Cfg,
		DB:   db,
		Auth: authSvc,
		Git:  &gitengine.Manager{GitBinary: gitBin, Root: t.TempDir()},
		MCP:  &kmcp.Server{DB: db, Auth: authSvc, Git: &gitengine.Manager{GitBinary: gitBin}, Log: log},
		Log:  log,
	}
	httpServer := &httptest.Server{Listener: ln, Config: &http.Server{Handler: app.Build()}}
	httpServer.Start()
	t.Cleanup(httpServer.Close)

	health, err := http.DefaultClient.Do(mustWebReq(t, http.MethodGet, base+"/healthz", http.NoBody))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.ReadAll(health.Body)
	_ = health.Body.Close()
	if health.StatusCode != http.StatusOK {
		t.Fatalf("healthz = %d", health.StatusCode)
	}
	home, err := http.DefaultClient.Do(mustWebReq(t, http.MethodGet, base+"/", http.NoBody))
	if err != nil {
		t.Fatal(err)
	}
	homeBody, _ := io.ReadAll(home.Body)
	_ = home.Body.Close()
	if home.StatusCode != http.StatusOK || !strings.Contains(string(homeBody), "Knotwork") {
		t.Fatalf("home = %d %s", home.StatusCode, homeBody)
	}
	meta, err := http.DefaultClient.Do(mustWebReq(t, http.MethodGet, base+"/.well-known/oauth-protected-resource/api/mcp", http.NoBody))
	if err != nil {
		t.Fatal(err)
	}
	metaBody, _ := io.ReadAll(meta.Body)
	_ = meta.Body.Close()
	if meta.StatusCode != http.StatusOK || !strings.Contains(string(metaBody), base+"/api/mcp") || strings.Contains(string(metaBody), "authorization_servers") {
		t.Fatalf("metadata = %d %s", meta.StatusCode, metaBody)
	}
	denied, err := http.DefaultClient.Do(mustWebReq(t, http.MethodPost, base+"/api/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`)))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.ReadAll(denied.Body)
	_ = denied.Body.Close()
	if denied.StatusCode != http.StatusUnauthorized || !strings.Contains(denied.Header.Get("WWW-Authenticate"), base+"/.well-known/oauth-protected-resource/api/mcp") {
		t.Fatalf("unauthenticated mcp = %d %q", denied.StatusCode, denied.Header.Get("WWW-Authenticate"))
	}

	cctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "knotwork-test", Version: "v1.8.0"}, nil).Connect(cctx, &mcp.StreamableClientTransport{
		Endpoint:   base + "/api/mcp",
		HTTPClient: &http.Client{Transport: headerTransport{base: http.DefaultTransport, token: token}},
	}, &mcp.ClientSessionOptions{ProtocolVersion: "2025-03-26"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cs.Close() }()
	res, err := cs.CallTool(cctx, &mcp.CallToolParams{Name: "get_mr_details", Arguments: map[string]any{
		"repo": repo.Path, "number": mr.Number,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Content) != 1 {
		t.Fatalf("content = %#v", res.Content)
	}

	// The legacy SSE route must flush its endpoint event through the web
	// stack's status recorder. The handler stays open until the client leaves.
	sctx, scancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer scancel()
	sseReq, err := http.NewRequestWithContext(sctx, http.MethodGet, base+"/api/mcp/sse", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	sseReq.Header.Set("Authorization", "Bearer "+token)
	sseReq.Header.Set("Accept", "text/event-stream")
	sseRes, err := http.DefaultClient.Do(sseReq)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sseRes.Body.Close() }()
	if sseRes.StatusCode != http.StatusOK || sseRes.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("sse = %d %q", sseRes.StatusCode, sseRes.Header.Get("Content-Type"))
	}
	buf := make([]byte, 512)
	n, err := sseRes.Body.Read(buf)
	if n == 0 && err != nil {
		t.Fatal(err)
	}
	event := string(buf[:n])
	if !strings.Contains(event, "event: endpoint") || !strings.Contains(event, "/api/mcp/messages?session=") {
		t.Fatalf("sse event = %q", event)
	}
}

func mustWebReq(t *testing.T, method, url string, body io.Reader) *http.Request {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, url, body)
	if err != nil {
		t.Fatal(err)
	}
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
	}
	return req
}

type headerTransport struct {
	base  http.RoundTripper
	token string
}

func (h headerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	clone := r.Clone(r.Context())
	clone.Header.Set("Authorization", "Bearer "+h.token)
	return h.base.RoundTrip(clone)
}

func testWebKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return key
}
