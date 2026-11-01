// Package gitengine adapts host Git to Knotwork: bare-repository lifecycle,
// Smart HTTP serving via `git http-backend`, and hook-to-daemon IPC.
package gitengine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	ErrPathTraversal   = errors.New("access denied: path traversal attempt")
	repoSegmentPattern = regexp.MustCompile(`^[a-zA-Z0-9_\-]+$`)
)

// SafeGitEnv returns an isolated environment for executing Git child processes.
// It wipes ambient inherited environment variables, disables global and system
// configuration files, prevents interactive terminal prompts, restricts network
// transport protocols to safe schemes (http, https), and establishes a minimal,
// controlled PATH.
func SafeGitEnv(gitBinary string) []string {
	path := "/usr/bin:/bin"
	if gitBinary != "" {
		dir := filepath.Dir(gitBinary)
		if dir != "." && dir != "" && dir != "/usr/bin" && dir != "/bin" {
			path = dir + ":" + path
		}
	}
	return []string{
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
		"PATH=" + path,
		"GIT_ALLOW_PROTOCOL=http:https",
		"GIT_PROTOCOL_FROM_USER=0",
	}
}

// ConcurrencyLimiter restricts concurrent Git CLI and transport processes to prevent
// resource exhaustion (RAM, CPU, and file descriptors).
type ConcurrencyLimiter struct {
	sem chan struct{}
}

func NewConcurrencyLimiter(maxConcurrent int) *ConcurrencyLimiter {
	if maxConcurrent <= 0 {
		maxConcurrent = 16
	}
	return &ConcurrencyLimiter{
		sem: make(chan struct{}, maxConcurrent),
	}
}

func (l *ConcurrencyLimiter) Acquire(ctx context.Context) error {
	select {
	case l.sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (l *ConcurrencyLimiter) Release() {
	select {
	case <-l.sem:
	default:
	}
}

type Manager struct {
	Root       string
	GitBinary  string
	SelfBinary string
	SocketPath string
	HooksDir   string
	Limiter    *ConcurrencyLimiter
}

func NewManager(root, gitBinary, selfBinary, socketPath string) (*Manager, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(abs, 0o750); err != nil {
		return nil, err
	}
	if gitBinary == "" {
		gitBinary = "git"
	}
	if resolved, err := exec.LookPath(gitBinary); err == nil {
		gitBinary = resolved
	}
	return &Manager{
		Root:       abs,
		GitBinary:  gitBinary,
		SelfBinary: selfBinary,
		SocketPath: socketPath,
		HooksDir:   filepath.Join(abs, ".hooks"),
		Limiter:    NewConcurrencyLimiter(16),
	}, nil
}

// Command constructs an *exec.Cmd targeting the configured Git binary running in
// an isolated environment free of ambient gitconfigs or dangerous protocols.
func (m *Manager) Command(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, m.GitBinary, args...)
	cmd.Env = SafeGitEnv(m.GitBinary)
	return cmd
}

// DiskPath returns the on-disk bare repository location for owner/name.
// Path segments are validated against ^[a-zA-Z0-9_\-]+$ and normalized to prevent
// directory traversal attacks.
func (m *Manager) DiskPath(path string) (string, error) {
	trimmed := strings.TrimSpace(path)
	if strings.HasPrefix(trimmed, "/") || filepath.IsAbs(trimmed) {
		return "", fmt.Errorf("invalid repository path %q: absolute paths not allowed", path)
	}
	clean := filepath.Clean(trimmed)
	if clean == "." || clean == "" || filepath.IsAbs(clean) {
		return "", fmt.Errorf("invalid repository path %q", path)
	}
	normalized := filepath.ToSlash(clean)
	segments := strings.Split(normalized, "/")
	for _, seg := range segments {
		if !repoSegmentPattern.MatchString(seg) || strings.HasPrefix(seg, "-") {
			return "", fmt.Errorf("invalid repository path %q: segment %q must match ^[a-zA-Z0-9_\\-]+$", path, seg)
		}
	}
	storageRoot := filepath.Clean(m.Root)
	targetPath := filepath.Clean(filepath.Join(storageRoot, clean+".git"))
	if !strings.HasPrefix(targetPath, storageRoot+string(filepath.Separator)) {
		return "", ErrPathTraversal
	}
	return targetPath, nil
}

// Init creates a bare repository with restricted filesystem permissions (0750 dir,
// 0640 files), enforces strict object sanitation (transfer/receive/fetch fsckObjects),
// caps maximum inbound packfile size, and installs static server hooks. It is
// idempotent: an existing repository has its configs and hooks updated.
func (m *Manager) Init(ctx context.Context, path string) (string, error) {
	disk, err := m.DiskPath(path)
	if err != nil {
		return "", err
	}
	if fi, err := os.Stat(disk); err == nil && fi.IsDir() {
		if err := m.configureRepo(ctx, disk); err != nil {
			return "", err
		}
		return disk, m.InstallHooks(disk)
	}
	if err := os.MkdirAll(filepath.Dir(disk), 0o750); err != nil {
		return "", err
	}
	cmd := m.Command(ctx, "init", "--bare", "--shared=0640", "--initial-branch=main", disk)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git init: %w: %s", err, strings.TrimSpace(string(out)))
	}
	//nolint:gosec // bare repository directory requires 0750 permissions for restricted group access.
	_ = os.Chmod(disk, 0o750)
	if err := m.configureRepo(ctx, disk); err != nil {
		return "", err
	}
	if err := m.InstallHooks(disk); err != nil {
		return "", err
	}
	return disk, nil
}

func (m *Manager) configureRepo(ctx context.Context, diskPath string) error {
	configs := [][2]string{
		{"receive.fsckObjects", "true"},
		{"fetch.fsckObjects", "true"},
		{"transfer.fsckObjects", "true"},
		{"receive.maxInputSize", "104857600"},
	}
	for _, cfg := range configs {
		cmd := m.Command(ctx, "-C", diskPath, "config", cfg[0], cfg[1])
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("git config %s: %w: %s", cfg[0], err, strings.TrimSpace(string(out)))
		}
	}
	return nil
}

// IsAncestor reports whether oldSHA is an ancestor of newSHA (a fast-forward).
func (m *Manager) IsAncestor(ctx context.Context, diskPath, oldSHA, newSHA string) bool {
	if oldSHA == "" || strings.Trim(oldSHA, "0") == "" {
		return true
	}
	if err := ValidateRef(oldSHA); err != nil {
		return false
	}
	if err := ValidateRef(newSHA); err != nil {
		return false
	}
	cmd := m.Command(ctx, "-C", diskPath, "merge-base", "--is-ancestor", "--end-of-options", oldSHA, newSHA)
	return cmd.Run() == nil
}

// InstallHooks provisions or updates hooks for a bare repository.
// Hooks are static, tamper-proof scripts deployed with read-only permissions (0555).
// In addition to setting core.hooksPath to a centralized read-only directory, hooks
// are installed in the repository's hooks/ directory.
func (m *Manager) InstallHooks(diskPath string) error {
	self, err := selfBinary(m.SelfBinary)
	if err != nil {
		return err
	}
	pre := fmt.Sprintf(`#!/bin/sh
# Generated by Knotwork. Do not edit.
: "${KNOTWORK_SOCKET:=%s}"
export KNOTWORK_SOCKET

BIN=%s
if [ -n "${KNOTWORK_BINARY:-}" ] && [ -x "$KNOTWORK_BINARY" ]; then
	BIN="$KNOTWORK_BINARY"
fi
if [ ! -x "$BIN" ]; then
	if command -v knotwork >/dev/null 2>&1; then
		BIN="$(command -v knotwork)"
	fi
fi
exec "$BIN" hook pre-receive
`, shellQuote(m.SocketPath), shellQuote(self))
	post := fmt.Sprintf(`#!/bin/sh
# Generated by Knotwork. Do not edit.
: "${KNOTWORK_SOCKET:=%s}"
export KNOTWORK_SOCKET

BIN=%s
if [ -n "${KNOTWORK_BINARY:-}" ] && [ -x "$KNOTWORK_BINARY" ]; then
	BIN="$KNOTWORK_BINARY"
fi
if [ ! -x "$BIN" ]; then
	if command -v knotwork >/dev/null 2>&1; then
		BIN="$(command -v knotwork)"
	fi
fi
exec "$BIN" hook post-receive
`, shellQuote(m.SocketPath), shellQuote(self))

	// Centralized hooks directory outside mutable repository path
	hooksDir := m.HooksDir
	if hooksDir == "" {
		hooksDir = filepath.Join(m.Root, ".hooks")
	}
	_ = os.MkdirAll(hooksDir, 0o750)
	if err := writeExecutable(filepath.Join(hooksDir, "pre-receive"), pre); err != nil {
		return err
	}
	if err := writeExecutable(filepath.Join(hooksDir, "post-receive"), post); err != nil {
		return err
	}

	if diskPath != "" {
		_ = m.Command(context.Background(), "-C", diskPath, "config", "core.hooksPath", hooksDir).Run()

		hooks := filepath.Join(diskPath, "hooks")
		if err := os.MkdirAll(hooks, 0o750); err != nil {
			return err
		}
		if err := writeExecutable(filepath.Join(hooks, "pre-receive"), pre); err != nil {
			return err
		}
		if err := writeExecutable(filepath.Join(hooks, "post-receive"), post); err != nil {
			return err
		}
	}
	return nil
}

func selfBinary(configured string) (string, error) {
	if configured != "" {
		if strings.Contains(configured, "go-build") {
			if abs, err := filepath.Abs("bin/knotwork"); err == nil {
				if info, err := os.Stat(abs); err == nil && !info.IsDir() {
					return abs, nil
				}
			}
		}
		return filepath.Abs(configured)
	}
	self, err := os.Executable()
	if err != nil {
		return "", err
	}
	if strings.Contains(self, "go-build") {
		if abs, err := filepath.Abs("bin/knotwork"); err == nil {
			if info, err := os.Stat(abs); err == nil && !info.IsDir() {
				return abs, nil
			}
		}
	}
	return self, nil
}

func writeExecutable(path, body string) error {
	tmp := path + ".tmp"
	//nolint:gosec // hook scripts must be deployed with 0555 read-only permissions for safe Git execution.
	if err := os.WriteFile(tmp, []byte(body), 0o555); err != nil {
		return err
	}
	//nolint:gosec // hook scripts must be deployed with 0555 read-only permissions.
	if err := os.Chmod(tmp, 0o555); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// Prune deletes the bare repository directory for path.
func (m *Manager) Prune(path string) error {
	disk, err := m.DiskPath(path)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(disk, m.Root+string(os.PathSeparator)) {
		return errors.New("refusing to prune outside repository root")
	}
	return os.RemoveAll(disk)
}
