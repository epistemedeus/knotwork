// Package backup snapshots repositories (debounced `git bundle`) and the
// database (VACUUM INTO / pg_dump, zstd-compressed) without blocking requests.
package backup

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"knotwork/internal/database"
	"knotwork/internal/domain"
	"knotwork/internal/gitengine"

	"github.com/klauspost/compress/zstd"
)

type Manager struct {
	Store       Store
	DB          *database.DB
	Git         *gitengine.Manager
	Log         *slog.Logger
	Debounce    time.Duration
	Interval    time.Duration
	PostgresBin string

	mu     sync.Mutex
	timers map[string]*time.Timer

	stop chan struct{}
	wg   sync.WaitGroup
}

func NewManager(store Store, db *database.DB, git *gitengine.Manager, log *slog.Logger, debounce, interval time.Duration) *Manager {
	if debounce <= 0 {
		debounce = 30 * time.Second
	}
	if interval <= 0 {
		interval = 6 * time.Hour
	}
	return &Manager{
		Store: store, DB: db, Git: git, Log: log,
		Debounce: debounce, Interval: interval,
		PostgresBin: "pg_dump",
		timers:      map[string]*time.Timer{},
	}
}

func (m *Manager) Start() {
	m.stop = make(chan struct{})
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		ticker := time.NewTicker(m.Interval)
		defer ticker.Stop()
		for {
			select {
			case <-m.stop:
				return
			case <-ticker.C:
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
				if err := m.Snapshot(ctx); err != nil {
					m.Log.Error("database snapshot failed", "error", err)
				}
				cancel()
			}
		}
	}()
}

func (m *Manager) Close() {
	m.mu.Lock()
	for _, timer := range m.timers {
		timer.Stop()
	}
	m.timers = map[string]*time.Timer{}
	m.mu.Unlock()
	if m.stop != nil {
		close(m.stop)
	}
	m.wg.Wait()
}

// Trigger resets the per-repository debounce timer. It is called by the
// post-receive hook path and never blocks the Git push.
func (m *Manager) Trigger(repo domain.Repository) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if timer, ok := m.timers[repo.ID]; ok {
		timer.Reset(m.Debounce)
		return
	}
	m.timers[repo.ID] = time.AfterFunc(m.Debounce, func() {
		m.mu.Lock()
		delete(m.timers, repo.ID)
		m.mu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
		defer cancel()
		if err := m.BundleRepository(ctx, repo); err != nil {
			m.Log.Error("repository bundle failed", "repo", repo.Path, "error", err)
		}
	})
}

// BundleRepository writes a full bundle and uploads it with retries.
func (m *Manager) BundleRepository(ctx context.Context, repo domain.Repository) error {
	var lastErr error
	backoff := 2 * time.Second
	for attempt := 1; attempt <= 3; attempt++ {
		err := m.uploadBundle(ctx, repo)
		if err == nil {
			if err := m.DB.MarkBundled(ctx, repo.ID); err != nil {
				m.Log.Warn("mark bundled failed", "repo", repo.Path, "error", err)
			}
			return nil
		}
		lastErr = err
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		backoff *= 2
	}
	return lastErr
}

func (m *Manager) uploadBundle(ctx context.Context, repo domain.Repository) error {
	tmp, err := os.CreateTemp("", "knotwork-*.bundle")
	if err != nil {
		return err
	}
	path := tmp.Name()
	_ = tmp.Close()
	_ = os.Remove(path)
	defer func() { _ = os.Remove(path) }()

	cmd := m.Git.Command(ctx, "-C", repo.DiskPath, "bundle", "create", path, "--all")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git bundle: %w: %s", err, string(out))
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	key := fmt.Sprintf("repos/%s/bundles/%s.bundle", repo.ID, time.Now().UTC().Format("20060102T150405Z"))
	return m.Store.Put(ctx, key, f)
}

// Snapshot uploads a compressed database dump. SQLite uses VACUUM INTO so the
// database keeps serving reads and writes during the copy.
func (m *Manager) Snapshot(ctx context.Context) error {
	key := fmt.Sprintf("backups/db/%s", time.Now().UTC().Format("20060102T150405Z"))
	switch m.DB.Driver {
	case "sqlite":
		if m.DB.Path == "" {
			return nil
		}
		return m.snapshotSQLite(ctx, key+".sqlite.zst")
	case "postgres":
		return m.snapshotPostgres(ctx, key+".sql.zst")
	default:
		return fmt.Errorf("unsupported database driver %q", m.DB.Driver)
	}
}

func (m *Manager) snapshotSQLite(ctx context.Context, key string) error {
	tmp, err := os.CreateTemp("", "knotwork-*.db")
	if err != nil {
		return err
	}
	path := tmp.Name()
	_ = tmp.Close()
	_ = os.Remove(path)
	defer func() { _ = os.Remove(path) }()

	quoted := "'" + filepath.ToSlash(path) + "'"
	if _, err := m.DB.ExecContext(ctx, "VACUUM INTO "+quoted); err != nil {
		return fmt.Errorf("vacuum into: %w", err)
	}
	return m.uploadCompressed(ctx, key, func(w *zstd.Encoder) error {
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer func() { _ = f.Close() }()
		_, err = w.ReadFrom(f)
		return err
	})
}

func (m *Manager) snapshotPostgres(ctx context.Context, key string) error {
	return m.uploadCompressed(ctx, key, func(w *zstd.Encoder) error {
		binary := m.PostgresBin
		if binary == "" {
			binary = "pg_dump"
		}
		cmd := exec.CommandContext(ctx, binary, "--dbname="+m.DB.DSN, "--no-owner", "--no-acl")
		cmd.Stdout = w
		cmd.Stderr = os.Stderr
		return cmd.Run()
	})
}

func (m *Manager) uploadCompressed(ctx context.Context, key string, write func(*zstd.Encoder) error) error {
	tmp, err := os.CreateTemp("", "knotwork-*.zst")
	if err != nil {
		return err
	}
	path := tmp.Name()
	defer func() { _ = os.Remove(path) }()
	enc, err := zstd.NewWriter(tmp, zstd.WithEncoderLevel(zstd.SpeedDefault))
	if err != nil {
		_ = tmp.Close()
		return err
	}
	if err := write(enc); err != nil {
		_ = enc.Close()
		_ = tmp.Close()
		return err
	}
	if err := enc.Close(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	return m.Store.Put(ctx, key, f)
}
