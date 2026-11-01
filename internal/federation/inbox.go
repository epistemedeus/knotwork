package federation

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os/exec"
	"strings"

	"knotwork/internal/database"
	"knotwork/internal/domain"
	"knotwork/internal/gitengine"
)

// InboxService accepts ForgeFed activities from allowlisted peers only.
type InboxService struct {
	DB      *database.DB
	Git     *gitengine.Manager
	Log     *slog.Logger
	BaseURL string
	Key     ed25519.PrivateKey
	KeyID   string
}

type Activity struct {
	Type   string          `json:"type"`
	Actor  string          `json:"actor"`
	Target string          `json:"target"`
	Object json.RawMessage `json:"object"`
}

type FederatedMR struct {
	Type         string `json:"type"`
	ID           string `json:"id"`
	Title        string `json:"title"`
	Description  string `json:"description"`
	SourceRepo   string `json:"sourceRepo"`
	SourceBranch string `json:"sourceBranch"`
	TargetBranch string `json:"targetBranch"`
	HeadSHA      string `json:"headSha"`
}

func (s *InboxService) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /inbox", s.handleInbox)
	mux.HandleFunc("GET /actor", s.actor)
}

func (s *InboxService) handleInbox(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 2<<20))
	if err != nil {
		http.Error(w, "cannot read body", http.StatusBadRequest)
		return
	}
	var activity Activity
	if err := json.Unmarshal(body, &activity); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	actorURL, err := url.Parse(activity.Actor)
	if err != nil || actorURL.Host == "" {
		http.Error(w, "invalid actor", http.StatusBadRequest)
		return
	}
	peer, err := s.DB.GetPeer(r.Context(), actorURL.Host)
	if err != nil {
		http.Error(w, "federation peer not allowlisted", http.StatusForbidden)
		return
	}
	if peer.TrustStatus != domain.PeerAllowlisted {
		http.Error(w, "federation peer not allowlisted", http.StatusForbidden)
		return
	}
	pub, err := PublicKeyFromPEM(peer.PublicKeyPEM)
	if err != nil {
		s.Log.Warn("peer has no usable public key", "peer", peer.Domain, "error", err)
		http.Error(w, "federation peer key unavailable", http.StatusForbidden)
		return
	}
	if err := Verify(r, body, pub); err != nil {
		s.Log.Warn("federation signature rejected", "peer", peer.Domain, "error", err)
		http.Error(w, "invalid http signature", http.StatusUnauthorized)
		return
	}
	if !strings.EqualFold(activity.Type, "Offer") {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	if err := s.ingestOffer(r.Context(), peer, activity); err != nil {
		s.Log.Warn("federated offer rejected", "peer", peer.Domain, "error", err)
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func (s *InboxService) ingestOffer(ctx context.Context, peer domain.Peer, activity Activity) error {
	var mr FederatedMR
	if err := json.Unmarshal(activity.Object, &mr); err != nil {
		return fmt.Errorf("invalid merge request object")
	}
	if mr.SourceRepo == "" || mr.SourceBranch == "" || mr.TargetBranch == "" {
		return fmt.Errorf("merge request is missing sourceRepo, sourceBranch, or targetBranch")
	}
	source, err := url.Parse(mr.SourceRepo)
	if err != nil || source.Scheme != "https" || !strings.EqualFold(source.Host, peer.Domain) {
		return fmt.Errorf("source repository must be https on the peer domain")
	}
	target, err := url.Parse(activity.Target)
	if err != nil {
		return fmt.Errorf("invalid target")
	}
	localPath := strings.Trim(target.Path, "/")
	repo, err := s.DB.GetRepoByPath(ctx, localPath)
	if err != nil {
		return fmt.Errorf("target repository not found")
	}

	result, err := s.DB.CreateFederatedMR(ctx, repo.ID, "", mr.SourceRepo, mr.SourceBranch, mr.TargetBranch,
		mr.Title, mr.Description, activity.Actor, peer.Domain, mr.HeadSHA)
	if err != nil {
		return fmt.Errorf("could not record merge request")
	}

	ref := fmt.Sprintf("refs/federated/mr/%d", result.Number)
	cmd := exec.CommandContext(ctx, s.Git.GitBinary, "-C", repo.DiskPath, "fetch", "--no-tags",
		mr.SourceRepo, mr.SourceBranch+":"+ref)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("could not fetch federated ref: %s", strings.TrimSpace(string(out)))
	}

	// Federated CI never starts without a maintainer approval (US-4.2).
	if _, err := s.DB.CreatePipeline(ctx, repo.ID, result.ID, mr.HeadSHA, ref, domain.PipelinePendingApproval, true); err != nil {
		return fmt.Errorf("could not create approval-gated pipeline")
	}
	s.Log.Info("federated merge request ingested", "repo", repo.Path, "mr", result.Number, "peer", peer.Domain)
	return nil
}

func (s *InboxService) actor(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/activity+json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"@context":          []string{"https://www.w3.org/ns/activitystreams", "https://forgefed.org/ns"},
		"id":                s.BaseURL + "/actor",
		"type":              "Application",
		"inbox":             s.BaseURL + "/inbox",
		"preferredUsername": "knotwork",
		"publicKey": map[string]string{
			"id":           s.KeyID,
			"owner":        s.BaseURL + "/actor",
			"publicKeyPem": publicKeyPEM(s.Key),
		},
	})
}

func publicKeyPEM(priv ed25519.PrivateKey) string {
	pub := priv.Public().(ed25519.PublicKey)
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pub}))
}
