package ci

import (
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
)

var ErrPendingApproval = errors.New("pipeline requires maintainer approval")

// Executor clones a bare repository into a temporary workspace and runs jobs
// in stage order. It refuses to start pipelines that are pending approval.
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

	tmp, err := os.MkdirTemp("", "knotwork-ci-*")
	if err != nil {
		return fail(err)
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	workspace := filepath.Join(tmp, "src")
	if err := e.git(ctx, out, "clone", "--quiet", "--shared", repo.DiskPath, workspace); err != nil {
		return fail(err)
	}
	if pipeline.CommitSHA != "" {
		if err := e.git(ctx, out, "-C", workspace, "checkout", "--quiet", "--detach", pipeline.CommitSHA); err != nil {
			return fail(err)
		}
	}

	cfg, file, err := Load(workspace)
	if errors.Is(err, ErrNoConfig) {
		e.Hub.Publish(pipeline.ID, "[knotwork] no "+ConfigFile+" found; nothing to run\n")
		_ = e.DB.SetPipelineStatus(ctx, pipeline.ID, domain.PipelineSuccess)
		return nil
	}
	if err != nil {
		return fail(err)
	}
	e.Hub.Publish(pipeline.ID, "[knotwork] using "+file+"\n")

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

func (e *Executor) git(ctx context.Context, out io.Writer, args ...string) error {
	binary := e.GitBin
	if binary == "" {
		binary = "git"
	}
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Stdout = out
	cmd.Stderr = out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return nil
}
