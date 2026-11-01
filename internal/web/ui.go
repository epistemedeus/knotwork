package web

import (
	"net/http"
	"strconv"

	"knotwork/internal/domain"
)

// loadRepo resolves a repository path and enforces read access for HTML pages.
// It redirects anonymous users to login and hides private repositories.
func (s *Server) loadRepo(w http.ResponseWriter, r *http.Request) (domain.Repository, domain.User, string, bool) {
	path := r.PathValue("owner") + "/" + r.PathValue("repo")
	repo, err := s.DB.GetRepoByPath(r.Context(), path)
	if err != nil {
		http.NotFound(w, r)
		return domain.Repository{}, domain.User{}, "", false
	}
	user, authed := s.Auth.UserFromRequest(r)
	role := ""
	if authed {
		role, _ = s.DB.RepoRole(r.Context(), repo, user)
	}
	if repo.IsPrivate && role == "" {
		if !authed {
			//nolint:gosec // next is the request path on our own origin, not a user-supplied URL.
			http.Redirect(w, r, "/auth/login?next="+r.URL.Path, http.StatusFound)
			return domain.Repository{}, domain.User{}, "", false
		}
		http.NotFound(w, r)
		return domain.Repository{}, domain.User{}, "", false
	}
	return repo, user, role, true
}

func (s *Server) repoPage(w http.ResponseWriter, r *http.Request) {
	repo, user, role, ok := s.loadRepo(w, r)
	if !ok {
		return
	}
	mrs, _ := s.DB.ListMRs(r.Context(), repo.ID)
	pipelines, _ := s.DB.ListPipelines(r.Context(), repo.ID)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = repoPageTemplate.Execute(w, page{
		Title:      repo.Path + " · Knotwork",
		Page:       "repo",
		Authed:     user.ID != "",
		User:       user,
		Repo:       repo,
		Role:       role,
		CanWrite:   role == "admin" || role == "write",
		CloneURL:   s.Cfg.BaseURL + "/" + repo.Path + ".git",
		DefaultRef: repo.DefaultBranch,
		MergeReqs:  mrs,
		Pipelines:  pipelines,
	})
}

func (s *Server) mrPage(w http.ResponseWriter, r *http.Request) {
	repo, user, role, ok := s.loadRepo(w, r)
	if !ok {
		return
	}
	number, err := strconv.ParseInt(r.PathValue("number"), 10, 64)
	if err != nil || number <= 0 {
		http.NotFound(w, r)
		return
	}
	mr, err := s.DB.GetMRByNumber(r.Context(), repo.ID, number)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = mrPageTemplate.Execute(w, page{
		Title:    "#" + strconv.FormatInt(mr.Number, 10) + " " + mr.Title + " · " + repo.Path,
		Page:     "merge-request",
		Authed:   user.ID != "",
		User:     user,
		Repo:     repo,
		Role:     role,
		CanWrite: role == "admin" || role == "write",
		MR:       mr,
	})
}

func (s *Server) pipelinePage(w http.ResponseWriter, r *http.Request) {
	repo, user, role, ok := s.loadRepo(w, r)
	if !ok {
		return
	}
	pipeline, err := s.DB.GetPipeline(r.Context(), r.PathValue("id"))
	if err != nil || pipeline.RepoID != repo.ID {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = pipePageTemplate.Execute(w, page{
		Title:    "Pipeline · " + repo.Path,
		Page:     "pipeline",
		Authed:   user.ID != "",
		User:     user,
		Repo:     repo,
		Role:     role,
		CanWrite: role == "admin" || role == "write",
		Pipeline: pipeline,
	})
}
