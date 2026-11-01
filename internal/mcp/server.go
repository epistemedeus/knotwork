// Package mcp exposes a small Model Context Protocol server over SSE.
// Tools and resources are authenticated with the same bearer token as the API.
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"knotwork/internal/auth"
	"knotwork/internal/database"
	"knotwork/internal/domain"
	"knotwork/internal/gitengine"
)

type Server struct {
	DB   *database.DB
	Auth *auth.Service
	Git  *gitengine.Manager
	Log  *slog.Logger

	mu       sync.Mutex
	sessions map[string]domain.User
}

func (s *Server) Register(mux *http.ServeMux) {
	if s.sessions == nil {
		s.sessions = map[string]domain.User{}
	}
	mux.Handle("GET /api/mcp/sse", s.Auth.RequireUser(http.HandlerFunc(s.sse)))
	mux.Handle("POST /api/mcp/messages", s.Auth.RequireUser(http.HandlerFunc(s.messages)))
}

func (s *Server) sse(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.UserFromContext(r.Context())
	id := database.NewID()
	s.mu.Lock()
	s.sessions[id] = user
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.sessions, id)
		s.mu.Unlock()
	}()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	_, _ = fmt.Fprintf(w, "event: endpoint\ndata: /api/mcp/messages?session=%s\n\n", id)
	flusher.Flush()

	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			_, _ = fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		}
	}
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

func (s *Server) messages(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.UserFromContext(r.Context())
	session := r.URL.Query().Get("session")
	s.mu.Lock()
	owner, ok := s.sessions[session]
	s.mu.Unlock()
	if !ok || owner.ID != user.ID {
		http.Error(w, "unknown MCP session", http.StatusNotFound)
		return
	}
	var req rpcRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeRPC(w, rpcResponse{JSONRPC: "2.0", Error: &rpcError{Code: -32700, Message: "parse error"}})
		return
	}
	if req.ID == nil {
		w.WriteHeader(http.StatusAccepted) // notification
		return
	}
	result, rerr := s.dispatch(r.Context(), user, req)
	writeRPC(w, rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: result, Error: rerr})
}

func (s *Server) dispatch(ctx context.Context, user domain.User, req rpcRequest) (any, *rpcError) {
	switch req.Method {
	case "initialize":
		return map[string]any{
			"protocolVersion": "2025-03-26",
			"capabilities": map[string]any{
				"resources": map[string]any{},
				"tools":     map[string]any{},
			},
			"serverInfo": map[string]any{"name": "knotwork", "version": "0.1.0"},
		}, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		return map[string]any{"tools": tools()}, nil
	case "resources/list":
		return s.listResources(ctx, user)
	case "resources/read":
		var p struct {
			URI string `json:"uri"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, &rpcError{Code: -32602, Message: "invalid params"}
		}
		return s.readResource(ctx, user, p.URI)
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, &rpcError{Code: -32602, Message: "invalid params"}
		}
		return s.callTool(ctx, user, p.Name, p.Arguments)
	default:
		return nil, &rpcError{Code: -32601, Message: "method not found: " + req.Method}
	}
}

func tools() []map[string]any {
	return []map[string]any{
		{
			"name":        "get_mr_details",
			"description": "Get a merge request, including federated origin metadata.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"repo":   map[string]any{"type": "string", "description": "owner/repo"},
					"number": map[string]any{"type": "integer"},
				},
				"required": []string{"repo", "number"},
			},
		},
		{
			"name":        "create_review_comment",
			"description": "Create a review comment on a merge request.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"repo":   map[string]any{"type": "string"},
					"number": map[string]any{"type": "integer"},
					"path":   map[string]any{"type": "string"},
					"line":   map[string]any{"type": "integer"},
					"body":   map[string]any{"type": "string"},
				},
				"required": []string{"repo", "number", "path", "line", "body"},
			},
		},
	}
}

type toolArgs struct {
	Repo   string `json:"repo"`
	Number int64  `json:"number"`
	Path   string `json:"path"`
	Line   int    `json:"line"`
	Body   string `json:"body"`
}

func (s *Server) callTool(ctx context.Context, user domain.User, name string, raw json.RawMessage) (any, *rpcError) {
	var args toolArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, &rpcError{Code: -32602, Message: "invalid tool arguments"}
	}
	repo, err := s.DB.GetRepoByPath(ctx, args.Repo)
	if err != nil {
		return nil, &rpcError{Code: -32602, Message: "repository not found"}
	}
	role, err := s.DB.RepoRole(ctx, repo, user)
	if err != nil || role == "" {
		return nil, &rpcError{Code: -32001, Message: "repository access denied"}
	}
	mr, err := s.DB.GetMRByNumber(ctx, repo.ID, args.Number)
	if err != nil {
		return nil, &rpcError{Code: -32602, Message: "merge request not found"}
	}
	switch name {
	case "get_mr_details":
		return textContent(mr), nil
	case "create_review_comment":
		if role != "admin" && role != "write" {
			return nil, &rpcError{Code: -32001, Message: "write access required"}
		}
		if args.Path == "" || args.Body == "" {
			return nil, &rpcError{Code: -32602, Message: "path and body are required"}
		}
		id, err := s.DB.CreateReviewComment(ctx, mr.ID, user.ID, args.Path, args.Line, args.Body)
		if err != nil {
			return nil, &rpcError{Code: -32603, Message: "could not create comment"}
		}
		return textContent(map[string]string{"id": id}), nil
	default:
		return nil, &rpcError{Code: -32601, Message: "unknown tool " + name}
	}
}

func (s *Server) listResources(ctx context.Context, user domain.User) (any, *rpcError) {
	repos, err := s.DB.ListRepos(ctx)
	if err != nil {
		return nil, &rpcError{Code: -32603, Message: "could not list repositories"}
	}
	var resources []map[string]string
	for _, repo := range repos {
		role, _ := s.DB.RepoRole(ctx, repo, user)
		if role == "" {
			continue
		}
		uri := "knotwork://" + repo.Path + "/tree/HEAD"
		resources = append(resources, map[string]string{
			"uri":         uri,
			"name":        repo.Path,
			"description": "Repository tree at HEAD",
			"mimeType":    "text/plain",
		})
	}
	return map[string]any{"resources": resources}, nil
}

func (s *Server) readResource(ctx context.Context, user domain.User, uri string) (any, *rpcError) {
	rest := strings.TrimPrefix(uri, "knotwork://")
	rest = strings.TrimPrefix(rest, "forge://")
	parts := strings.SplitN(rest, "/", 3)
	if len(parts) != 3 {
		return nil, &rpcError{Code: -32602, Message: "invalid resource URI"}
	}
	repoPath := parts[0] + "/" + parts[1]
	repo, err := s.DB.GetRepoByPath(ctx, repoPath)
	if err != nil {
		return nil, &rpcError{Code: -32602, Message: "repository not found"}
	}
	role, _ := s.DB.RepoRole(ctx, repo, user)
	if role == "" {
		return nil, &rpcError{Code: -32001, Message: "repository access denied"}
	}
	switch {
	case strings.HasPrefix(parts[2], "tree/"):
		ref := strings.TrimPrefix(parts[2], "tree/")
		if err := gitengine.ValidateRef(ref); err != nil {
			return nil, &rpcError{Code: -32602, Message: "invalid git ref"}
		}
		paths, err := s.Git.ListTree(ctx, repo.DiskPath, ref)
		if err != nil {
			return nil, &rpcError{Code: -32603, Message: "could not read tree"}
		}
		return textContent(strings.Join(paths, "\n") + "\n"), nil
	case strings.HasPrefix(parts[2], "diff/"):
		number, err := strconv.ParseInt(strings.TrimPrefix(parts[2], "diff/"), 10, 64)
		if err != nil {
			return nil, &rpcError{Code: -32602, Message: "invalid merge request number"}
		}
		mr, err := s.DB.GetMRByNumber(ctx, repo.ID, number)
		if err != nil {
			return nil, &rpcError{Code: -32602, Message: "merge request not found"}
		}
		if err := gitengine.ValidateRef(mr.HeadSHA); err != nil {
			return nil, &rpcError{Code: -32602, Message: "invalid merge request head commit"}
		}
		patch, err := s.Git.DiffRange(ctx, repo.DiskPath, "", mr.HeadSHA)
		if err != nil {
			return nil, &rpcError{Code: -32603, Message: "could not read diff"}
		}
		return textContent(string(patch)), nil
	default:
		return nil, &rpcError{Code: -32602, Message: "unsupported resource"}
	}
}

func textContent(v any) map[string]any {
	var text string
	switch t := v.(type) {
	case string:
		text = t
	default:
		b, _ := json.MarshalIndent(v, "", "  ")
		text = string(b)
	}
	return map[string]any{"content": []map[string]any{{"type": "text", "text": text}}}
}

func writeRPC(w http.ResponseWriter, resp rpcResponse) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}
