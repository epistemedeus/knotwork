package gitengine

import (
	"context"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestQuarantineEnvValidation(t *testing.T) {
	root := t.TempDir()
	mgr, err := NewManager(root, "git", "/usr/bin/true", filepath.Join(root, "hook.sock"))
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	repoDisk, err := mgr.Init(context.Background(), "alice/repo")
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	server := NewHookServer(nil, mgr, slog.New(slog.NewTextHandler(io.Discard, nil)))

	objectsDir := filepath.Join(repoDisk, "objects")
	incomingDir := filepath.Join(objectsDir, "incoming-xyz123")
	if err := os.MkdirAll(incomingDir, 0o750); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	t.Run("valid quarantine directory", func(t *testing.T) {
		req := HookRequest{
			ObjectDir:     incomingDir,
			AltObjectDirs: objectsDir,
		}
		env := server.quarantineEnv(repoDisk, req)
		if len(env) == 0 {
			t.Fatal("quarantineEnv returned empty slice for valid incoming directory")
		}
		envMap := make(map[string]string)
		for _, e := range env {
			parts := strings.SplitN(e, "=", 2)
			if len(parts) == 2 {
				envMap[parts[0]] = parts[1]
			}
		}
		if got := envMap["GIT_OBJECT_DIRECTORY"]; got != incomingDir {
			t.Errorf("GIT_OBJECT_DIRECTORY = %q, want %q", got, incomingDir)
		}
		if got := envMap["GIT_ALTERNATE_OBJECT_DIRECTORIES"]; got != objectsDir {
			t.Errorf("GIT_ALTERNATE_OBJECT_DIRECTORIES = %q, want %q", got, objectsDir)
		}
	})

	t.Run("relative quarantine path resolved inside repo", func(t *testing.T) {
		req := HookRequest{
			ObjectDir: filepath.Join("objects", "incoming-xyz123"),
		}
		env := server.quarantineEnv(repoDisk, req)
		if len(env) == 0 {
			t.Fatal("quarantineEnv returned empty slice for relative incoming directory")
		}
		found := false
		for _, e := range env {
			if strings.HasPrefix(e, "GIT_OBJECT_DIRECTORY=") {
				found = true
				val := strings.TrimPrefix(e, "GIT_OBJECT_DIRECTORY=")
				if val != incomingDir {
					t.Errorf("GIT_OBJECT_DIRECTORY = %q, want %q", val, incomingDir)
				}
			}
		}
		if !found {
			t.Error("GIT_OBJECT_DIRECTORY missing from env")
		}
	})

	t.Run("rejects path traversal attempt", func(t *testing.T) {
		traversalReqs := []HookRequest{
			{ObjectDir: filepath.Join(repoDisk, "..", "..", "etc")},
			{ObjectDir: "/etc"},
			{ObjectDir: filepath.Join(repoDisk, "objects", "..", "..", "other")},
		}
		for _, req := range traversalReqs {
			env := server.quarantineEnv(repoDisk, req)
			if len(env) != 0 {
				t.Errorf("quarantineEnv accepted traversal attempt: %v (got %v)", req.ObjectDir, env)
			}
		}
	})

	t.Run("filters out malicious alt object directories", func(t *testing.T) {
		req := HookRequest{
			ObjectDir:     incomingDir,
			AltObjectDirs: "/etc:/var/secret:" + objectsDir,
		}
		env := server.quarantineEnv(repoDisk, req)
		for _, e := range env {
			if strings.HasPrefix(e, "GIT_ALTERNATE_OBJECT_DIRECTORIES=") {
				val := strings.TrimPrefix(e, "GIT_ALTERNATE_OBJECT_DIRECTORIES=")
				if strings.Contains(val, "/etc") || strings.Contains(val, "/var/secret") {
					t.Errorf("untrusted alternate paths were not filtered out: %q", val)
				}
				if !strings.Contains(val, objectsDir) {
					t.Errorf("canonical objects dir missing from alternates: %q", val)
				}
			}
		}
	})
}

func TestIsAncestorWithQuarantine(t *testing.T) {
	root := t.TempDir()
	mgr, err := NewManager(root, "git", "/usr/bin/true", filepath.Join(root, "hook.sock"))
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	repoDisk, err := mgr.Init(context.Background(), "alice/quarantine-test")
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	// Create a worktree repository to generate commits
	workDir := filepath.Join(root, "work")
	if err := os.MkdirAll(workDir, 0o750); err != nil {
		t.Fatalf("MkdirAll work: %v", err)
	}
	ctx := context.Background()
	run := func(dir string, args ...string) string {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Test",
			"GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=Test",
			"GIT_COMMITTER_EMAIL=test@example.com",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, string(out))
		}
		return strings.TrimSpace(string(out))
	}

	run(workDir, "init")
	run(workDir, "config", "user.name", "Test")
	run(workDir, "config", "user.email", "test@example.com")
	_ = os.WriteFile(filepath.Join(workDir, "file.txt"), []byte("commit 1"), 0o640)
	run(workDir, "add", "file.txt")
	run(workDir, "commit", "-m", "first commit")
	sha1 := run(workDir, "rev-parse", "HEAD")

	// Push first commit directly into bare repo (bypassing pre-receive since /usr/bin/true is used)
	run(workDir, "push", repoDisk, "HEAD:refs/heads/main")

	// Second commit in workdir
	_ = os.WriteFile(filepath.Join(workDir, "file.txt"), []byte("commit 2"), 0o640)
	run(workDir, "add", "file.txt")
	run(workDir, "commit", "-m", "second commit")
	sha2 := run(workDir, "rev-parse", "HEAD")

	// Place workdir's objects into an isolated quarantine folder inside bare repo
	quarantineDir := filepath.Join(repoDisk, "objects", "incoming-test")
	if err := os.MkdirAll(quarantineDir, 0o750); err != nil {
		t.Fatalf("MkdirAll quarantine: %v", err)
	}

	// Copy object files from workDir/.git/objects into quarantineDir
	copyDir(t, filepath.Join(workDir, ".git", "objects"), quarantineDir)

	// Without quarantine env: bare repo does NOT have sha2 yet, IsAncestor must fail
	if mgr.IsAncestor(ctx, repoDisk, sha1, sha2) {
		t.Fatalf("IsAncestor succeeded without quarantine env, expected failure")
	}

	// With quarantine env: bare repo finds sha2 in quarantine, IsAncestor must succeed!
	objectsDir := filepath.Join(repoDisk, "objects")
	server := NewHookServer(nil, mgr, slog.New(slog.NewTextHandler(io.Discard, nil)))
	env := server.quarantineEnv(repoDisk, HookRequest{
		ObjectDir:     quarantineDir,
		AltObjectDirs: objectsDir,
	})

	if !mgr.IsAncestor(ctx, repoDisk, sha1, sha2, env...) {
		t.Fatalf("IsAncestor failed with quarantine env, expected success")
	}
}

func TestCentralHooksProvisioning(t *testing.T) {
	root := t.TempDir()
	mgr, err := NewManager(root, "git", "/bin/sh", filepath.Join(root, "hook.sock"))
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	// EnsureCentralHooks creates executable pre-receive in mgr.HooksDir
	if err := mgr.EnsureCentralHooks(); err != nil {
		t.Fatalf("EnsureCentralHooks: %v", err)
	}

	preReceive := filepath.Join(mgr.HooksDir, "pre-receive")
	info, err := os.Stat(preReceive)
	if err != nil {
		t.Fatalf("pre-receive hook does not exist: %v", err)
	}
	if info.Mode()&0o111 == 0 {
		t.Fatalf("pre-receive hook is not executable: %v", info.Mode())
	}

	// Obsolete post-receive must not exist
	if _, err := os.Stat(filepath.Join(mgr.HooksDir, "post-receive")); err == nil {
		t.Fatal("post-receive hook should not exist")
	}
}

func TestPopApprovedRefs(t *testing.T) {
	server := NewHookServer(nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	diskPath := "/path/to/repo.git"

	refs := []RefUpdate{
		{Old: "abc", New: "def", Ref: "refs/heads/main"},
	}

	server.mu.Lock()
	server.approvedRefs[diskPath] = refs
	server.mu.Unlock()

	popped := server.PopApprovedRefs(diskPath)
	if len(popped) != 1 || popped[0].New != "def" {
		t.Fatalf("PopApprovedRefs = %v, want %v", popped, refs)
	}

	// Second pop must be empty
	second := server.PopApprovedRefs(diskPath)
	if len(second) != 0 {
		t.Fatalf("second PopApprovedRefs = %v, want empty", second)
	}
}

func copyDir(t *testing.T, src, dst string) {
	t.Helper()
	entries, err := os.ReadDir(src)
	if err != nil {
		t.Fatalf("ReadDir %s: %v", src, err)
	}
	for _, entry := range entries {
		s := filepath.Join(src, entry.Name())
		d := filepath.Join(dst, entry.Name())
		if entry.IsDir() {
			if err := os.MkdirAll(d, 0o750); err != nil {
				t.Fatalf("MkdirAll %s: %v", d, err)
			}
			copyDir(t, s, d)
		} else {
			data, err := os.ReadFile(s)
			if err != nil {
				t.Fatalf("ReadFile %s: %v", s, err)
			}
			if err := os.WriteFile(d, data, 0o640); err != nil {
				t.Fatalf("WriteFile %s: %v", d, err)
			}
		}
	}
}
