package gitengine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path"
	"strings"
)

var (
	ErrInvalidRef  = errors.New("invalid git ref")
	ErrPathMissing = errors.New("path not found in revision")
)

// ValidateRef rejects rev arguments that could be interpreted as options or
// that escape a plain revision namespace.
func ValidateRef(ref string) error {
	ref = strings.TrimSpace(ref)
	if ref == "" || ref == "HEAD" {
		return nil
	}
	if strings.HasPrefix(ref, "-") || strings.ContainsAny(ref, "\x00\n\r\t :?[\\^~*") || strings.Contains(ref, "..") {
		return ErrInvalidRef
	}
	if strings.HasPrefix(ref, "/") || strings.HasSuffix(ref, "/") || strings.HasSuffix(ref, ".") {
		return ErrInvalidRef
	}
	return nil
}

// ValidatePath rejects absolute paths and traversal when reading a blob.
func ValidatePath(p string) error {
	if p == "" || strings.HasPrefix(p, "/") || strings.HasPrefix(p, "-") || strings.ContainsRune(p, '\x00') {
		return ErrPathMissing
	}
	clean := path.Clean(p)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return ErrPathMissing
	}
	return nil
}

// ListTree returns every blob path in ref, sorted by Git.
func (m *Manager) ListTree(ctx context.Context, diskPath, ref string) ([]string, error) {
	if ref == "" {
		ref = "HEAD"
	}
	if err := ValidateRef(ref); err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, m.GitBinary, "-C", diskPath, "ls-tree", "-r", "-z", "--name-only", ref, "--")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git ls-tree: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	entries := strings.Split(string(out), "\x00")
	paths := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry != "" {
			paths = append(paths, entry)
		}
	}
	return paths, nil
}

// ReadBlob returns the contents of path at ref together with a binary flag.
func (m *Manager) ReadBlob(ctx context.Context, diskPath, ref, filePath string) ([]byte, bool, error) {
	if ref == "" {
		ref = "HEAD"
	}
	if err := ValidateRef(ref); err != nil {
		return nil, false, err
	}
	if err := ValidatePath(filePath); err != nil {
		return nil, false, err
	}
	cmd := exec.CommandContext(ctx, m.GitBinary, "-C", diskPath, "show", ref+":"+filePath)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if strings.Contains(stderr.String(), "does not exist") || strings.Contains(stderr.String(), "exists on disk") {
			return nil, false, ErrPathMissing
		}
		return nil, false, fmt.Errorf("git show: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return out, isBinary(out), nil
}

func isBinary(data []byte) bool {
	sample := data
	if len(sample) > 8000 {
		sample = sample[:8000]
	}
	return bytes.IndexByte(sample, 0) >= 0
}

// DiffRange returns a unified patch between base and head. When base is empty
// the commit itself is shown as a patch.
func (m *Manager) DiffRange(ctx context.Context, diskPath, base, head string) ([]byte, error) {
	if err := ValidateRef(head); err != nil {
		return nil, err
	}
	var args []string
	if base != "" {
		if err := ValidateRef(base); err != nil {
			return nil, err
		}
		args = []string{"-C", diskPath, "diff", "--no-color", "--no-ext-diff", base + "..." + head, "--"}
	} else {
		args = []string{"-C", diskPath, "show", "--format=", "--no-color", "--no-ext-diff", "--patch", head, "--"}
	}
	cmd := exec.CommandContext(ctx, m.GitBinary, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git diff: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// ResolveCommit returns the first candidate that names a commit in diskPath.
func (m *Manager) ResolveCommit(ctx context.Context, diskPath string, candidates ...string) (string, error) {
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		if err := ValidateRef(candidate); err != nil {
			continue
		}
		cmd := exec.CommandContext(ctx, m.GitBinary, "-C", diskPath, "rev-parse", "--verify", "--quiet", candidate+"^{commit}")
		out, err := cmd.Output()
		if err == nil {
			if sha := strings.TrimSpace(string(out)); sha != "" {
				return sha, nil
			}
		}
	}
	return "", errors.New("no matching revision")
}
