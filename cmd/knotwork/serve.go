package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"knotwork/internal/auth"
	"knotwork/internal/backup"
	"knotwork/internal/ci"
	"knotwork/internal/config"
	"knotwork/internal/database"
	"knotwork/internal/domain"
	"knotwork/internal/federation"
	"knotwork/internal/gitengine"
	"knotwork/internal/mcp"
	"knotwork/internal/web"
)

func runServe(args []string) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "knotwork:", err)
		return 1
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := os.MkdirAll(cfg.DataDir, 0o750); err != nil {
		log.Error("create data directory failed", "error", err, "dir", cfg.DataDir)
		return 1
	}

	store, err := database.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Error("database open failed", "error", err)
		return 1
	}
	defer func() { _ = store.Close() }()

	self, err := os.Executable()
	if err != nil {
		log.Error("resolve executable", "error", err)
		return 1
	}
	if strings.Contains(self, "go-build") {
		if abs, err := filepath.Abs("bin/knotwork"); err == nil {
			if info, err := os.Stat(abs); err == nil && !info.IsDir() {
				self = abs
			}
		}
	}
	gitMgr, err := gitengine.NewManager(cfg.RepoRoot, cfg.GitBinary, self, cfg.SocketPath)
	if err != nil {
		log.Error("git engine init failed", "error", err)
		return 1
	}

	hookServer := gitengine.NewHookServer(store, gitMgr, log)
	if err := hookServer.Start(cfg.SocketPath); err != nil {
		log.Error("hook socket failed", "error", err, "socket", cfg.SocketPath)
		return 1
	}
	defer func() { _ = hookServer.Close() }()

	blobStore, err := backup.NewStore(ctx, cfg.S3, filepath.Join(cfg.DataDir, "backups"))
	if err != nil {
		log.Error("backup store init failed", "error", err)
		return 1
	}
	backupMgr := backup.NewManager(blobStore, store, gitMgr, log, cfg.BundleDebounce, cfg.BackupInterval)
	backupMgr.Start()
	defer backupMgr.Close()

	authSvc, err := auth.NewService(ctx, cfg, store, log)
	if err != nil {
		log.Error("auth init failed", "error", err)
		return 1
	}

	hub := ci.NewHub()
	executor := &ci.Executor{
		DB:     store,
		GitBin: cfg.GitBinary,
		Hub:    hub,
		Log:    log,
		Runner: &ci.DockerRunner{Binary: "docker", Host: cfg.DockerHost},
	}

	hookServer.OnPostReceive = func(ctx context.Context, repo domain.Repository, refs []gitengine.RefUpdate) {
		backupMgr.Trigger(repo)
		seen := map[string]bool{}
		for _, update := range refs {
			if !strings.HasPrefix(update.Ref, "refs/heads/") || isZeroSHA(update.New) || seen[update.New] {
				continue
			}
			seen[update.New] = true
			pipeline, err := store.CreatePipeline(ctx, repo.ID, "", update.New, update.Ref, domain.PipelineQueued, false)
			if err != nil {
				log.Warn("could not create pipeline", "repo", repo.Path, "ref", update.Ref, "error", err)
				continue
			}
			//nolint:gosec // the pipeline must outlive the hook request; cancel is bounded by the timeout.
			go func(p domain.Pipeline) {
				runCtx, cancel := context.WithTimeout(context.Background(), time.Hour)
				defer cancel()
				if err := executor.RunPipeline(runCtx, p); err != nil && !errors.Is(err, context.Canceled) {
					log.Error("pipeline failed", "pipeline", p.ID, "error", err)
				}
			}(pipeline)
		}
	}

	fed := &federation.InboxService{
		DB:      store,
		Git:     gitMgr,
		Log:     log,
		BaseURL: cfg.BaseURL,
		Key:     authSvc.Key,
		KeyID:   cfg.BaseURL + "/actor#main-key",
	}
	mcpServer := &mcp.Server{DB: store, Auth: authSvc, Git: gitMgr, Log: log}

	app := &web.Server{
		Cfg: cfg, DB: store, Auth: authSvc, Git: gitMgr, Hook: hookServer,
		CI: executor, Hub: hub, Backup: backupMgr, Fed: fed, MCP: mcpServer, Log: log,
	}
	handler := app.Build()

	setupDone, _ := store.SetupCompleted(ctx)
	if !setupDone {
		fmt.Printf("Setup key: %s\nVisit: %s/setup?key=%s\n", cfg.SetupSecret, cfg.BaseURL, cfg.SetupSecret)
	}

	httpServer := &http.Server{
		Addr:              cfg.Addr,
		Handler:           handler,
		ReadHeaderTimeout: 15 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}

	errCh := make(chan error, 1)
	go func() {
		if cfg.TLSCert != "" {
			errCh <- httpServer.ListenAndServeTLS(cfg.TLSCert, cfg.TLSKey)
			return
		}
		errCh <- httpServer.ListenAndServe()
	}()
	log.Info("knotwork listening", "addr", cfg.Addr, "base_url", cfg.BaseURL, "db", store.Driver)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	select {
	case s := <-sig:
		log.Info("shutting down", "signal", s.String())
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			log.Error("http server stopped", "error", err)
			return 1
		}
	}
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()
	_ = httpServer.Shutdown(shutdownCtx)
	return 0
}

func isZeroSHA(sha string) bool {
	if sha == "" {
		return true
	}
	return strings.Trim(sha, "0") == ""
}
