package gitengine

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
)

// RunHook is the entrypoint for `knotwork hook pre-receive` or direct symlink invocation.
// Pre-receive fails closed: if the daemon socket is unavailable the push is
// rejected rather than silently bypassing branch protection.
func RunHook(kind string, stdin io.Reader, stdout, stderr io.Writer) int {
	if kind != "pre-receive" {
		_, _ = fmt.Fprintf(stderr, "knotwork: unsupported hook %q\n", kind)
		return 1
	}
	refs, err := parseRefUpdates(stdin)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "knotwork: %v\n", err)
		return 1
	}
	dir, err := os.Getwd()
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "knotwork: cannot determine repository directory")
		return 1
	}
	repoDir := os.Getenv("KNOTWORK_REPO_PATH")
	if repoDir == "" {
		repoDir = dir
	}
	req := HookRequest{
		Kind:           kind,
		RepoDir:        repoDir,
		ActorID:        os.Getenv("KNOTWORK_ACTOR_ID"),
		Refs:           refs,
		ObjectDir:      os.Getenv("GIT_OBJECT_DIRECTORY"),
		AltObjectDirs:  os.Getenv("GIT_ALTERNATE_OBJECT_DIRECTORIES"),
		QuarantinePath: os.Getenv("GIT_QUARANTINE_PATH"),
	}
	resp, err := DialHook(os.Getenv("KNOTWORK_SOCKET"), req)
	if err != nil {
		_, _ = fmt.Fprintln(stdout, "Remote rejected: internal validation unavailable")
		return 1
	}
	if resp.Message != "" {
		_, _ = fmt.Fprintln(stdout, resp.Message)
	}
	if !resp.OK {
		return 1
	}
	return 0
}

func parseRefUpdates(r io.Reader) ([]RefUpdate, error) {
	scanner := bufio.NewScanner(r)
	var refs []RefUpdate
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 3 {
			continue
		}
		refs = append(refs, RefUpdate{Old: fields[0], New: fields[1], Ref: fields[2]})
	}
	return refs, scanner.Err()
}
