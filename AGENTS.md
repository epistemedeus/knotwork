# AGENTS.md

Knotwork is a single-node Git collaboration server in Go with a vanilla
TypeScript UI. Module path is `knotwork`; the binary and CLI are `knotwork`.

## Verified commands

Run tooling through mise (`mise exec -- ...`):

```sh
mise install
mise exec -- go build ./...          # all packages
mise exec -- go build -o bin/knotwork ./cmd/knotwork
mise exec -- go test ./...           # full suite
mise exec -- go test ./internal/ci -run TestParse -v   # single test
mise exec -- go vet ./...
mise exec -- golangci-lint run ./...
```

Frontend (from `frontend/`):

```sh
mise exec -- pnpm install
mise exec -- pnpm build     # tsc --noEmit && vite build
mise exec -- pnpm typecheck
```

`pnpm build` writes `internal/web/static/{app.js,app.css,chunks/}`, which is
committed because `internal/web/static.go` embeds it with `go:embed`. Rebuild
the frontend before `go build` if assets changed; plain `go build ./...` still
works from a clean checkout because the output is committed.

## Layout

- `cmd/knotwork/` — `serve`, `admin`, `auth login`, `hook` entrypoints.
- `internal/domain` — dependency-free entities/constants.
- `internal/database` — store and embedded migrations (`0001_init.sql`).
- `internal/auth` — OIDC, sessions, JWTs, device flow, SCIM, break-glass.
- `internal/gitengine` — bare repos, hooks, hook IPC, Smart HTTP, browsing.
- `internal/federation` — signatures, allowlisted inbox, ForgeFed.
- `internal/ci` — parser, Docker runner, log hub, executor.
- `internal/backup` — git bundles and database snapshots.
- `internal/mcp`, `internal/manifest` — MCP server and app manifests.
- `internal/web` — router, JSON API, HTML templates, static assets.
- `frontend/` — Oat CSS, `@pierre/trees`, `@pierre/diffs`, `@pierre/highlights`.

`docs/SRS.md` is authoritative; `docs/ARCHITECTURE.md` maps request flows.

## Quirks

- Environment variables are `KNOTWORK_*`. `KNOTWORK_DATA_DIR`, not `DATA_DIR`.
- OIDC is disabled until issuer, client ID, and secret are all set; `/auth/login`
  then returns 503. There is no password login by design.
- Git Smart HTTP is registered as catch-all `GET /` and `POST /` in
  `internal/web/server.go` because `/{owner}/{repo}.git/...` cannot coexist with
  other two-segment mux patterns. `gitengine.parseGitRequest` filters them.
- The daemon must own the Unix socket at `KNOTWORK_SOCKET_PATH`; generated hook
  scripts exec `knotwork hook <kind>`. Hooks fail closed if it is missing.
- `git http-backend` is invoked through `net/http/cgi`; `gitengine.NewManager`
  resolves `git` to an absolute path because CGI does not search `PATH`.
- SQLite uses `MaxOpenConns(1)` plus WAL. `database.Open` creates the parent
  directory for file-backed SQLite.
- CI config prefers `.knotwork-ci.yml` and falls back to `.forge-ci.yml`.
- Federation accepts only allowlisted peers with valid Ed25519 HTTP signatures.
- pnpm 12 needs `frontend/pnpm-workspace.yaml` (`allowBuilds: esbuild`) and
  `minimumReleaseAge: 0` because `@knadh/oat` releases frequently.
- The web bundle includes all Shiki language chunks (~12 MB). Initial JS is
  `app.js`; language chunks load on demand.
- Templates are Go `html/template` sets built by `newPage` in
  `internal/web/templates.go`; each page supplies a `content` block.
- Do not add inline styles/scripts to templates; the CSP allows only
  same-origin scripts and the Vite bundle.
