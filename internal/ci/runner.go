package ci

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

type PipelineJob struct {
	ID        string
	ProjectID string
	CommitSHA string
	Image     string
	Commands  []string
	Workspace string
}

type JobResult struct {
	ExitCode int
	Duration time.Duration
}

// Runner is the pluggable CI backend boundary.
type Runner interface {
	Run(ctx context.Context, job *PipelineJob, log io.Writer) (*JobResult, error)
}

// DockerRunner executes jobs in ephemeral, unprivileged containers. It never
// passes --privileged and uses bridge networking (US-4.1).
type DockerRunner struct {
	Binary string
	Host   string
}

func (d *DockerRunner) Run(ctx context.Context, job *PipelineJob, out io.Writer) (*JobResult, error) {
	binary := d.Binary
	if binary == "" {
		binary = "docker"
	}
	script := strings.Join(job.Commands, "\n")
	args := []string{
		"run", "--rm",
		"--network", "bridge",
		"-v", job.Workspace + ":/workspace:rw",
		"-w", "/workspace",
		job.Image,
		"sh", "-euc", script,
	}
	cmd := exec.CommandContext(ctx, binary, args...)
	if d.Host != "" {
		cmd.Env = append(os.Environ(), "DOCKER_HOST="+d.Host)
	}
	cmd.Stdout = out
	cmd.Stderr = out
	start := time.Now()
	err := cmd.Run()
	result := &JobResult{Duration: time.Since(start)}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		result.ExitCode = exitErr.ExitCode()
		return result, nil
	}
	if err != nil {
		return nil, fmt.Errorf("docker run: %w", err)
	}
	return result, nil
}
