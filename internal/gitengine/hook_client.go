package gitengine

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
)

// RunHook is the entrypoint for `knotwork hook pre-receive|post-receive`.
// Pre-receive fails closed: if the daemon socket is unavailable the push is
// rejected rather than silently bypassing branch protection.
func RunHook(kind string, stdin io.Reader, stdout, stderr io.Writer) int {
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
	req := HookRequest{
		Kind:    kind,
		RepoDir: dir,
		ActorID: os.Getenv("KNOTWORK_ACTOR_ID"),
		Refs:    refs,
	}
	resp, err := DialHook(os.Getenv("KNOTWORK_SOCKET"), req)
	if err != nil {
		if kind == "post-receive" {
			_, _ = fmt.Fprintf(stderr, "knotwork: post-receive event skipped: %v\n", err)
			return 0
		}
		_, _ = fmt.Fprintln(stdout, "Remote rejected: internal validation unavailable")
		return 1
	}
	if resp.Message != "" {
		_, _ = fmt.Fprintln(stdout, resp.Message)
	}
	if !resp.OK {
		if kind == "post-receive" {
			return 0
		}
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
