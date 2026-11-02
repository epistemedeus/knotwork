package gitengine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"knotwork/internal/database"
	"knotwork/internal/domain"
)

// RefUpdate is one line of a Git pre-receive transaction.
type RefUpdate struct {
	Old string `json:"old"`
	New string `json:"new"`
	Ref string `json:"ref"`
}

type HookRequest struct {
	Kind           string      `json:"kind"` // "pre-receive" or "post-receive"
	RepoDir        string      `json:"repo_dir"`
	ActorID        string      `json:"actor_id"`
	Refs           []RefUpdate `json:"refs"`
	ObjectDir      string      `json:"object_dir,omitempty"`
	AltObjectDirs  string      `json:"alt_object_dirs,omitempty"`
	QuarantinePath string      `json:"quarantine_path,omitempty"`
}

type HookResponse struct {
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

// HookServer validates pushes over a Unix domain socket so Git hooks never
// touch the database directly.
type HookServer struct {
	store   *database.DB
	manager *Manager
	log     *slog.Logger

	OnPostReceive func(ctx context.Context, repo domain.Repository, refs []RefUpdate)

	mu   sync.Mutex
	ln   net.Listener
	done chan struct{}
}

func NewHookServer(store *database.DB, manager *Manager, log *slog.Logger) *HookServer {
	return &HookServer{store: store, manager: manager, log: log}
}

func (s *HookServer) Start(socketPath string) error {
	_ = os.Remove(socketPath)
	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "unix", socketPath)
	if err != nil {
		return err
	}
	//nolint:gosec // 0660 lets the git user and the daemon group share the socket.
	if err := os.Chmod(socketPath, 0o660); err != nil {
		_ = ln.Close()
		return err
	}
	s.mu.Lock()
	s.ln = ln
	s.done = make(chan struct{})
	s.mu.Unlock()

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				select {
				case <-s.done:
					return
				default:
					s.log.Warn("hook socket accept failed", "error", err)
					return
				}
			}
			go s.handle(conn)
		}
	}()
	return nil
}

func (s *HookServer) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ln == nil {
		return nil
	}
	close(s.done)
	return s.ln.Close()
}

func (s *HookServer) handle(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))

	var req HookRequest
	if err := json.NewDecoder(conn).Decode(&req); err != nil {
		s.log.Warn("invalid hook request", "error", err)
		return
	}
	resp := s.authorize(req)
	if err := json.NewEncoder(conn).Encode(resp); err != nil {
		s.log.Warn("write hook response", "error", err)
	}
}

func (s *HookServer) authorize(req HookRequest) HookResponse {
	ctx := context.Background()
	repo, err := s.store.GetRepoByDiskPath(ctx, req.RepoDir)
	if err != nil {
		return HookResponse{Message: "Remote rejected: unknown repository"}
	}
	user, err := s.store.GetUserByID(ctx, req.ActorID)
	if err != nil || !user.IsActive {
		return HookResponse{Message: "Remote rejected: unknown or disabled user"}
	}
	role, err := s.store.RepoRole(ctx, repo, user)
	if err != nil {
		return HookResponse{Message: "Remote rejected: permission lookup failed"}
	}
	if role == "" || role == "read" {
		return HookResponse{Message: "Remote rejected: write access denied"}
	}

	if req.Kind == "post-receive" {
		if s.OnPostReceive != nil {
			s.OnPostReceive(ctx, repo, req.Refs)
		}
		return HookResponse{OK: true}
	}
	if req.Kind != "pre-receive" {
		return HookResponse{Message: "Remote rejected: unknown hook"}
	}

	protected := splitBranches(repo.ProtectedBranches)
	quarantineEnv := s.quarantineEnv(repo.DiskPath, req)
	for _, update := range req.Refs {
		branch, ok := strings.CutPrefix(update.Ref, "refs/heads/")
		if !ok || !protected[branch] {
			continue
		}
		if !user.IsAdmin() && role != "admin" {
			return HookResponse{Message: fmt.Sprintf("Remote rejected: Branch [%s] is protected", branch)}
		}
		if update.New == strings.Repeat("0", len(update.New)) {
			return HookResponse{Message: fmt.Sprintf("Remote rejected: Branch [%s] cannot be deleted", branch)}
		}
		if !s.manager.IsAncestor(ctx, repo.DiskPath, update.Old, update.New, quarantineEnv...) {
			return HookResponse{Message: fmt.Sprintf("Remote rejected: non-fast-forward push to [%s]", branch)}
		}
	}
	return HookResponse{OK: true}
}

func (s *HookServer) quarantineEnv(repoDiskPath string, req HookRequest) []string {
	if req.ObjectDir == "" {
		return nil
	}
	cleanObj := filepath.Clean(req.ObjectDir)
	if !filepath.IsAbs(cleanObj) {
		cleanObj = filepath.Clean(filepath.Join(repoDiskPath, cleanObj))
	}
	objectsRoot := filepath.Clean(filepath.Join(repoDiskPath, "objects"))

	realObj := resolvePath(cleanObj)
	realRoot := resolvePath(objectsRoot)

	// Quarantine directory must reside strictly within the repository's objects folder
	if realObj != realRoot && !strings.HasPrefix(realObj, realRoot+string(filepath.Separator)) {
		s.log.Warn("quarantine object dir outside repository objects", "dir", req.ObjectDir, "repo", repoDiskPath)
		return nil
	}

	env := []string{"GIT_OBJECT_DIRECTORY=" + cleanObj}
	var validAlts []string
	if req.AltObjectDirs != "" {
		for _, alt := range filepath.SplitList(req.AltObjectDirs) {
			cleanAlt := filepath.Clean(alt)
			if !filepath.IsAbs(cleanAlt) {
				cleanAlt = filepath.Clean(filepath.Join(repoDiskPath, cleanAlt))
			}
			realAlt := resolvePath(cleanAlt)
			if realAlt == realRoot || strings.HasPrefix(realAlt, realRoot+string(filepath.Separator)) {
				validAlts = append(validAlts, cleanAlt)
			}
		}
	}
	if len(validAlts) == 0 {
		validAlts = []string{objectsRoot}
	}
	env = append(env, "GIT_ALTERNATE_OBJECT_DIRECTORIES="+strings.Join(validAlts, string(filepath.ListSeparator)))

	if req.QuarantinePath != "" {
		cleanQuar := filepath.Clean(req.QuarantinePath)
		if !filepath.IsAbs(cleanQuar) {
			cleanQuar = filepath.Clean(filepath.Join(repoDiskPath, cleanQuar))
		}
		realQuar := resolvePath(cleanQuar)
		if realQuar == realRoot || strings.HasPrefix(realQuar, realRoot+string(filepath.Separator)) {
			env = append(env, "GIT_QUARANTINE_PATH="+cleanQuar)
		}
	}

	return env
}

func resolvePath(p string) string {
	clean := filepath.Clean(p)
	if resolved, err := filepath.EvalSymlinks(clean); err == nil {
		return resolved
	}
	return clean
}

func splitBranches(csv string) map[string]bool {
	out := map[string]bool{}
	for _, b := range strings.Split(csv, ",") {
		if b = strings.TrimSpace(b); b != "" {
			out[b] = true
		}
	}
	return out
}

// DialHook sends one request to the daemon's hook socket.
func DialHook(socketPath string, req HookRequest) (HookResponse, error) {
	if socketPath == "" {
		return HookResponse{}, errors.New("KNOTWORK_SOCKET is not set")
	}
	conn, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(context.Background(), "unix", socketPath)
	if err != nil {
		return HookResponse{}, err
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return HookResponse{}, err
	}
	var resp HookResponse
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return HookResponse{}, err
	}
	return resp, nil
}
