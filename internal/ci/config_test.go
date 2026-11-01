package ci

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseOrdersJobsByStage(t *testing.T) {
	cfg, err := Parse([]byte(`
stages: [build, test]
compile:
  stage: build
  image: golang:1.27
  script: go build ./...
lint:
  stage: test
  image: golang:1.27
  script:
    - go vet ./...
    - go test ./...
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(cfg.Jobs) != 2 {
		t.Fatalf("jobs = %d, want 2", len(cfg.Jobs))
	}
	if cfg.Jobs[0].Name != "compile" || cfg.Jobs[1].Name != "lint" {
		t.Fatalf("unexpected order: %+v", cfg.Jobs)
	}
	if got := cfg.Jobs[1].Script; len(got) != 2 || got[0] != "go vet ./..." {
		t.Fatalf("script = %v", got)
	}
}

func TestParseDefaultsStageAndValidates(t *testing.T) {
	cfg, err := Parse([]byte("test:\n  image: alpine\n  script: echo ok\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.Jobs[0].Stage != "test" {
		t.Fatalf("stage = %q, want test", cfg.Jobs[0].Stage)
	}

	for name, body := range map[string]string{
		"missing image":  "job:\n  script: echo ok\n",
		"missing script": "job:\n  image: alpine\n",
		"unknown stage":  "stages: [build]\njob:\n  stage: deploy\n  image: alpine\n  script: echo ok\n",
		"no jobs":        "stages: [build]\n",
	} {
		if _, err := Parse([]byte(body)); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

func TestLoadPrefersKnotworkFile(t *testing.T) {
	dir := t.TempDir()
	legacy := "stages: [test]\nlegacy:\n  image: alpine\n  script: echo legacy\n"
	current := "stages: [test]\ncurrent:\n  image: alpine\n  script: echo current\n"
	if err := os.WriteFile(filepath.Join(dir, legacyConfigFile), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ConfigFile), []byte(current), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, file, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if file != ConfigFile {
		t.Fatalf("file = %q, want %q", file, ConfigFile)
	}
	if cfg.Jobs[0].Name != "current" {
		t.Fatalf("job = %q, want current", cfg.Jobs[0].Name)
	}
}

func TestLoadFallsBackToLegacyAndReportsMissing(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := Load(dir); err != ErrNoConfig {
		t.Fatalf("err = %v, want ErrNoConfig", err)
	}
	body := "job:\n  image: alpine\n  script: echo legacy\n"
	if err := os.WriteFile(filepath.Join(dir, legacyConfigFile), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, file, err := Load(dir); err != nil || file != legacyConfigFile {
		t.Fatalf("file = %q, err = %v", file, err)
	}
}
