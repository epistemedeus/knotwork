package web

import (
	"html/template"

	"knotwork/internal/domain"
)

type repoEntry struct {
	Path        string
	Description string
	Private     bool
}

// page is the single view model shared by every HTML template.
type page struct {
	Title  string
	Page   string
	Authed bool
	User   domain.User

	Repo       domain.Repository
	Role       string
	CanWrite   bool
	CloneURL   string
	DefaultRef string
	TreeURL    string

	Repos     []repoEntry
	MergeReqs []domain.MergeRequest
	Pipelines []domain.Pipeline
	MR        domain.MergeRequest
	Pipeline  domain.Pipeline
}

const baseLayout = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}}</title>
<link rel="stylesheet" href="/static/app.css">
<script type="module" src="/static/app.js"></script>
</head>
<body data-page="{{.Page}}">
<header class="container site-header">
<nav>
<a href="/" class="brand">Knotwork</a>
<span class="spacer"></span>
{{if .Authed}}
<span class="meta">{{.User.Username}}{{if .User.IsAdmin}} · admin{{end}}</span>
<a href="/auth/logout">Sign out</a>
{{else}}
<a href="/auth/login">Sign in</a>
{{end}}
</nav>
</header>
<main class="container">
{{template "content" .}}
</main>
<footer class="container meta site-footer">Knotwork 0.1.0 · single-node, federated Git collaboration</footer>
</body>
</html>`

const homeContent = `{{define "content"}}
<section class="hero">
<h1>Knotwork</h1>
<p class="lead">Self-hosted Git collaboration with OIDC sign-in, merge requests, embedded CI, and opt-in federation.</p>
{{if not .Authed}}<p><a href="/auth/login" role="button">Sign in with OIDC</a></p>{{end}}
</section>

<section>
<h2>Repositories</h2>
{{if .Repos}}
<div class="repo-grid">
{{range .Repos}}
<article class="card">
<h3><a href="/repos/{{.Path}}">{{.Path}}</a></h3>
{{if .Private}}<span class="badge">private</span>{{else}}<span class="badge">public</span>{{end}}
{{if .Description}}<p class="meta">{{.Description}}</p>{{end}}
</article>
{{end}}
</div>
{{else}}
<p class="meta">No visible repositories yet. Create one with <code>POST /api/repos</code>.</p>
{{end}}
</section>
{{end}}`

const repoContent = `{{define "content"}}
<nav class="breadcrumb"><a href="/">Repositories</a> / <span>{{.Repo.Path}}</span></nav>

<header class="repo-header">
<div>
<h1>{{.Repo.Path}}</h1>
<p class="meta">
{{if .Repo.IsPrivate}}<span class="badge">private</span>{{else}}<span class="badge">public</span>{{end}}
{{if .Role}} · your role: {{.Role}}{{end}}
</p>
</div>
</header>

{{if .Repo.Description}}<p>{{.Repo.Description}}</p>{{end}}

<details class="clone">
<summary>Clone</summary>
<pre><code>git clone {{.CloneURL}}</code></pre>
</details>

<section class="repo-browser">
<aside class="tree-pane">
<h2 class="pane-title">Files</h2>
<div id="file-tree" data-repo="{{.Repo.Path}}" data-ref="{{.DefaultRef}}">
<p class="meta">Loading tree…</p>
</div>
</aside>
<section class="file-pane" id="file-view">
<p class="meta">Select a file to view it.</p>
</section>
</section>

<div class="columns">
<section>
<h2>Merge requests</h2>
{{if .MergeReqs}}
<ul class="item-list">
{{range .MergeReqs}}
<li>
<a href="/repos/{{$.Repo.Path}}/merge-requests/{{.Number}}">#{{.Number}} {{.Title}}</a>
{{if .IsFederated}}<span class="badge">federated · {{.OriginDomain}}</span>{{end}}
<span class="meta">{{.State}} · {{.SourceBranch}} → {{.TargetBranch}}</span>
</li>
{{end}}
</ul>
{{else}}
<p class="meta">No merge requests.</p>
{{end}}
</section>

<section>
<h2>Pipelines</h2>
{{if .Pipelines}}
<ul class="item-list">
{{range .Pipelines}}
<li>
<a href="/repos/{{$.Repo.Path}}/pipelines/{{.ID}}">{{.RefName}} @ {{shortSHA .CommitSHA}}</a>
<span class="badge">{{.Status}}</span>
{{if .RequiresApproval}}<span class="meta">approval required</span>{{end}}
</li>
{{end}}
</ul>
{{else}}
<p class="meta">No pipelines yet.</p>
{{end}}
</section>
</div>
{{end}}`

const mrContent = `{{define "content"}}
<nav class="breadcrumb"><a href="/">Repositories</a> / <a href="/repos/{{.Repo.Path}}">{{.Repo.Path}}</a> / <span>#{{.MR.Number}}</span></nav>

<header>
<h1>#{{.MR.Number}} {{.MR.Title}}</h1>
<p class="meta">
<span class="badge">{{.MR.State}}</span>
{{if .MR.IsFederated}}<span class="badge">federated · {{.MR.OriginDomain}}</span>{{end}}
{{.MR.SourceBranch}} → {{.MR.TargetBranch}}
</p>
</header>

{{if .MR.Description}}<article class="card"><p>{{.MR.Description}}</p></article>{{end}}

<section>
<h2>Changes</h2>
<div id="diff-view" data-repo="{{.Repo.Path}}" data-number="{{.MR.Number}}">
<p class="meta">Loading diff…</p>
</div>
</section>
{{end}}`

const pipelineContent = `{{define "content"}}
<nav class="breadcrumb"><a href="/">Repositories</a> / <a href="/repos/{{.Repo.Path}}">{{.Repo.Path}}</a> / <span>pipeline</span></nav>

<header>
<h1>Pipeline {{shortSHA .Pipeline.CommitSHA}}</h1>
<p class="meta">
Status: <strong id="pipeline-status">{{.Pipeline.Status}}</strong>
· ref {{.Pipeline.RefName}}
{{if .Pipeline.RequiresApproval}}<span class="badge">approval required</span>{{end}}
</p>
</header>

{{if and .Pipeline.RequiresApproval .CanWrite}}
<p><button id="approve-pipeline" data-pipeline="{{.Pipeline.ID}}" data-repo="{{.Repo.Path}}">Approve and run pipeline</button></p>
{{end}}

<pre id="pipeline-log" class="log" data-pipeline="{{.Pipeline.ID}}"></pre>
{{end}}`

func newPage(content string) *template.Template {
	return template.Must(template.New("layout").Funcs(template.FuncMap{
		"shortSHA": func(sha string) string {
			if len(sha) > 8 {
				return sha[:8]
			}
			if sha == "" {
				return "unknown"
			}
			return sha
		},
	}).Parse(baseLayout + content))
}

var (
	homeTemplate     = newPage(homeContent)
	repoPageTemplate = newPage(repoContent)
	mrPageTemplate   = newPage(mrContent)
	pipePageTemplate = newPage(pipelineContent)
)
