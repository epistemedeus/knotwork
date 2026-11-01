package gitengine

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func newRequest(target string) *http.Request {
	return httptest.NewRequest("GET", target, nil)
}

func gitCmd(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestRepositoryRoundTrip(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	mgr, err := NewManager(root, "git", "", filepath.Join(root, "hook.sock"))
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	disk, err := mgr.Init(ctx, "alice/demo")
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	for _, hook := range []string{"pre-receive", "post-receive"} {
		info, err := os.Stat(filepath.Join(disk, "hooks", hook))
		if err != nil || info.Mode()&0o111 == 0 {
			t.Fatalf("hook %s missing or not executable: %v", hook, err)
		}
	}
	// The generated hooks expect a live daemon socket; disable them for the
	// fixture pushes while still exercising the real bare repository.
	gitCmd(t, disk, "config", "core.hooksPath", os.DevNull)

	work := t.TempDir()
	gitCmd(t, work, "init", "-q", "-b", "main")
	gitCmd(t, work, "config", "user.email", "test@example.com")
	gitCmd(t, work, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(work, "README.md"), []byte("# Demo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "app.js"), []byte("console.log('hi')\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, work, "add", ".")
	gitCmd(t, work, "commit", "-qm", "initial")
	gitCmd(t, work, "remote", "add", "origin", disk)
	gitCmd(t, work, "push", "-q", "origin", "main")

	paths, err := mgr.ListTree(ctx, disk, "main")
	if err != nil {
		t.Fatalf("ListTree: %v", err)
	}
	if len(paths) != 2 || paths[0] != "README.md" {
		t.Fatalf("paths = %v", paths)
	}
	content, binary, err := mgr.ReadBlob(ctx, disk, "main", "app.js")
	if err != nil || binary || string(content) != "console.log('hi')\n" {
		t.Fatalf("ReadBlob = %q, %v, %v", content, binary, err)
	}

	if _, _, err := mgr.ReadBlob(ctx, disk, "main", "../secret"); err == nil {
		t.Fatal("path traversal was allowed")
	}
	if _, err := mgr.ListTree(ctx, disk, "--help"); err == nil {
		t.Fatal("option-like ref was allowed")
	}
	if _, err := mgr.ListTree(ctx, disk, "missing-branch"); err == nil {
		t.Fatal("missing ref should fail")
	}

	mainSHA, err := mgr.ResolveCommit(ctx, disk, "main")
	if err != nil || len(mainSHA) != 40 {
		t.Fatalf("ResolveCommit = %q, %v", mainSHA, err)
	}
	if _, err := mgr.ResolveCommit(ctx, disk, "does-not-exist"); err == nil {
		t.Fatal("unknown revision resolved")
	}

	gitCmd(t, work, "checkout", "-qb", "feature")
	if err := os.WriteFile(filepath.Join(work, "app.js"), []byte("console.log('bye')\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, work, "commit", "-qam", "update")
	gitCmd(t, work, "push", "-q", "origin", "feature")

	patch, err := mgr.DiffRange(ctx, disk, "main", "feature")
	if err != nil {
		t.Fatalf("DiffRange: %v", err)
	}
	if !strings.Contains(string(patch), "console.log('bye')") || !strings.Contains(string(patch), "a/app.js") {
		t.Fatalf("unexpected patch:\n%s", patch)
	}
}

func TestValidateRef(t *testing.T) {
	valid := []string{"", "HEAD", "main", "refs/heads/main", "refs/federated/mr/1", "a1b2c3"}
	for _, ref := range valid {
		if err := ValidateRef(ref); err != nil {
			t.Errorf("ValidateRef(%q) = %v", ref, err)
		}
	}
	invalid := []string{"-x", "main..dev", "main branch", "refs/heads/main:evil", "a^b", "a~1"}
	for _, ref := range invalid {
		if err := ValidateRef(ref); err == nil {
			t.Errorf("ValidateRef(%q) accepted", ref)
		}
	}
}

func TestValidatePath(t *testing.T) {
	for _, p := range []string{"", "/etc/passwd", "../secret", "src/../../secret", "-rf"} {
		if err := ValidatePath(p); err == nil {
			t.Errorf("ValidatePath(%q) accepted", p)
		}
	}
	for _, p := range []string{"README.md", "src/main.go", "a/../b"} {
		if err := ValidatePath(p); err != nil {
			t.Errorf("ValidatePath(%q) = %v", p, err)
		}
	}
}

func TestParseGitRequest(t *testing.T) {
	req, ok := parseGitRequest(newRequest("/alice/demo.git/info/refs?service=git-receive-pack"))
	if !ok || req.owner != "alice" || req.repo != "demo" || !req.write {
		t.Fatalf("receive info/refs = %+v, %v", req, ok)
	}
	req, ok = parseGitRequest(newRequest("/alice/demo.git/git-upload-pack"))
	if !ok || req.write || req.endpoint != "git-upload-pack" {
		t.Fatalf("upload-pack = %+v, %v", req, ok)
	}
	if _, ok := parseGitRequest(newRequest("/alice/demo/info/refs")); ok {
		t.Fatal("non-.git path accepted")
	}
	if _, ok := parseGitRequest(newRequest("/alice/demo.git/unknown")); ok {
		t.Fatal("unknown endpoint accepted")
	}
}
