package ci

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"knotwork/internal/database"
	"knotwork/internal/domain"
	"knotwork/internal/gitengine"
)

var ErrPendingApproval = errors.New("pipeline requires maintainer approval")

// Executor inspects CI configuration directly from the Git object store without
// checking out files on the host. If jobs are present, repository content is
// safely unpacked via git archive into a temporary workspace for Docker isolation.
type Executor struct {
	DB     *database.DB
	GitBin string
	Runner Runner
	Hub    *Hub
	Log    *slog.Logger
}

func (e *Executor) RunPipeline(ctx context.Context, pipeline domain.Pipeline) error {
	if pipeline.Status == domain.PipelinePendingApproval {
		return ErrPendingApproval
	}
	if pipeline.Status == domain.PipelineRunning ||
		pipeline.Status == domain.PipelineSuccess ||
		pipeline.Status == domain.PipelineFailed {
		return nil
	}
	repo, err := e.DB.GetRepoByID(ctx, pipeline.RepoID)
	if err != nil {
		return fmt.Errorf("load repository: %w", err)
	}
	if err := e.DB.SetPipelineStatus(ctx, pipeline.ID, domain.PipelineRunning); err != nil {
		return err
	}
	out := e.Hub.Writer(pipeline.ID)
	fail := func(err error) error {
		e.Hub.Publish(pipeline.ID, "\n[knotwork] pipeline failed: "+err.Error()+"\n")
		_ = e.DB.SetPipelineStatus(context.WithoutCancel(ctx), pipeline.ID, domain.PipelineFailed)
		return err
	}

	commitRef := pipeline.CommitSHA
	if commitRef == "" {
		commitRef = "HEAD"
	}
	if err := gitengine.ValidateRef(commitRef); err != nil {
		return fail(fmt.Errorf("invalid commit ref: %w", err))
	}

	// Read CI configuration directly from the object database without checkout
	cfg, file, err := e.loadConfigFromRepo(ctx, repo.DiskPath, commitRef)
	if errors.Is(err, ErrNoConfig) {
		e.Hub.Publish(pipeline.ID, "[knotwork] no "+ConfigFile+" found; nothing to run\n")
		_ = e.DB.SetPipelineStatus(ctx, pipeline.ID, domain.PipelineSuccess)
		return nil
	}
	if err != nil {
		return fail(err)
	}
	e.Hub.Publish(pipeline.ID, "[knotwork] using "+file+"\n")

	// Create ephemeral workspace and unpack commit via git archive (no git checkout)
	tmp, err := os.MkdirTemp("", "knotwork-ci-*")
	if err != nil {
		return fail(err)
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	workspace := filepath.Join(tmp, "src")
	if err := os.MkdirAll(workspace, 0o750); err != nil {
		return fail(err)
	}
	if err := e.extractArchive(ctx, repo.DiskPath, commitRef, workspace); err != nil {
		return fail(fmt.Errorf("extract commit tree: %w", err))
	}

	runner := e.Runner
	if runner == nil {
		runner = &DockerRunner{}
	}
	for _, job := range cfg.Jobs {
		e.Hub.Publish(pipeline.ID, fmt.Sprintf("\n[knotwork] job %s (stage %s, image %s)\n", job.Name, job.Stage, job.Image))
		result, err := runner.Run(ctx, &PipelineJob{
			ID:        pipeline.ID + ":" + job.Name,
			ProjectID: repo.ID,
			CommitSHA: pipeline.CommitSHA,
			Image:     job.Image,
			Commands:  job.Script,
			Workspace: workspace,
		}, out)
		if err != nil {
			return fail(err)
		}
		if result.ExitCode != 0 {
			return fail(fmt.Errorf("job %s exited with status %d", job.Name, result.ExitCode))
		}
		if len(job.Artifacts.Paths) > 0 {
			e.Hub.Publish(pipeline.ID, "[knotwork] artifact collection is not implemented yet\n")
		}
	}
	e.Hub.Publish(pipeline.ID, "\n[knotwork] pipeline succeeded\n")
	return e.DB.SetPipelineStatus(ctx, pipeline.ID, domain.PipelineSuccess)
}

func (e *Executor) loadConfigFromRepo(ctx context.Context, diskPath, commitRef string) (Config, string, error) {
	binary := e.GitBin
	if binary == "" {
		binary = "git"
	}
	for _, name := range []string{ConfigFile, legacyConfigFile} {
		cmd := exec.CommandContext(ctx, binary, "--git-dir", diskPath, "cat-file", "-p", "--", commitRef+":"+name)
		cmd.Env = gitengine.SafeGitEnv(binary)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err == nil {
			cfg, err := Parse(out)
			if err != nil {
				return Config{}, name, fmt.Errorf("job config parse: %w", err)
			}
			return cfg, name, nil
		}
	}
	return Config{}, "", ErrNoConfig
}

// extractArchive safely extracts the git tree directly from the object database without checkout.
func (e *Executor) extractArchive(ctx context.Context, diskPath, commitRef, targetDir string) error {
	binary := e.GitBin
	if binary == "" {
		binary = "git"
	}
	cmd := exec.CommandContext(ctx, binary, "--git-dir", diskPath, "archive", "--format=tar", "--end-of-options", commitRef)
	cmd.Env = gitengine.SafeGitEnv(binary)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("git archive start: %w", err)
	}

	tr := tar.NewReader(stdout)
	targetClean := filepath.Clean(targetDir)

	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			_ = cmd.Wait()
			return fmt.Errorf("tar read: %w", err)
		}
		rel := filepath.Clean(header.Name)
		if rel == "." || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
			continue
		}
		dest := filepath.Clean(filepath.Join(targetClean, rel))
		if !strings.HasPrefix(dest, targetClean+string(filepath.Separator)) {
			continue
		}

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(dest, 0o750); err != nil {
				_ = cmd.Wait()
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(dest), 0o750); err != nil {
				_ = cmd.Wait()
				return err
			}
			mode := header.FileInfo().Mode().Perm()
			f, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
			if err != nil {
				_ = cmd.Wait()
				return err
			}
			// Limit individual file decompression to 100MB to prevent zip/tar bombs.
			if _, err := io.Copy(f, io.LimitReader(tr, 100<<20)); err != nil {
				_ = f.Close()
				_ = cmd.Wait()
				return err
			}
			_ = f.Close()
		case tar.TypeSymlink:
			linkTarget := header.Linkname
			if filepath.IsAbs(linkTarget) {
				continue
			}
			destDir := filepath.Dir(dest)
			//nolint:gosec // destination and resolved symlink path are verified against targetClean directory prefix.
			resolved := filepath.Clean(filepath.Join(destDir, linkTarget))
			if !strings.HasPrefix(resolved, targetClean+string(filepath.Separator)) {
				continue
			}
			_ = os.MkdirAll(destDir, 0o750)
			_ = os.Remove(dest)
			_ = os.Symlink(linkTarget, dest)
		}
	}

	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("git archive wait: %w", err)
	}
	return nil
}
