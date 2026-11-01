package ci

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// ErrNoConfig means the repository has no pipeline file.
var ErrNoConfig = errors.New("no pipeline configuration")

// ConfigFile is the preferred filename; ForgeLite's original name is still read.
const ConfigFile = ".knotwork-ci.yml"

var legacyConfigFile = ".forge-ci.yml"

type Config struct {
	Stages []string `json:"stages"`
	Jobs   []Job    `json:"jobs"`
}

type Job struct {
	Name      string    `json:"name"`
	Stage     string    `json:"stage"`
	Image     string    `json:"image"`
	Script    []string  `json:"script"`
	Artifacts Artifacts `json:"artifacts,omitempty"`
}

type Artifacts struct {
	Paths []string `yaml:"paths" json:"paths,omitempty"`
}

type stringList []string

func (s *stringList) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind == yaml.ScalarNode {
		*s = []string{value.Value}
		return nil
	}
	var out []string
	if err := value.Decode(&out); err != nil {
		return err
	}
	*s = out
	return nil
}

type jobYAML struct {
	Stage     string     `yaml:"stage"`
	Image     string     `yaml:"image"`
	Script    stringList `yaml:"script"`
	Artifacts Artifacts  `yaml:"artifacts"`
}

// Parse accepts the documented GitLab-CI subset: stages, image, script, artifacts.
func Parse(body []byte) (Config, error) {
	var raw map[string]any
	if err := yaml.Unmarshal(body, &raw); err != nil {
		return Config{}, err
	}
	cfg := Config{}
	if v, ok := raw["stages"]; ok {
		list, ok := v.([]any)
		if !ok {
			return Config{}, fmt.Errorf("stages must be a list")
		}
		for _, item := range list {
			stage, ok := item.(string)
			if !ok || stage == "" {
				return Config{}, fmt.Errorf("stage names must be non-empty strings")
			}
			cfg.Stages = append(cfg.Stages, stage)
		}
	}
	if len(cfg.Stages) == 0 {
		cfg.Stages = []string{"test"}
	}
	stageIndex := map[string]int{}
	for i, s := range cfg.Stages {
		stageIndex[s] = i
	}
	defaultStage := cfg.Stages[0]
	for _, s := range cfg.Stages {
		if s == "test" {
			defaultStage = "test"
		}
	}

	names := make([]string, 0, len(raw))
	for name, value := range raw {
		if name == "stages" || strings.HasPrefix(name, ".") {
			continue
		}
		if _, ok := value.(map[string]any); !ok {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		encoded, err := yaml.Marshal(raw[name])
		if err != nil {
			return Config{}, err
		}
		var j jobYAML
		if err := yaml.Unmarshal(encoded, &j); err != nil {
			return Config{}, fmt.Errorf("job %q: %w", name, err)
		}
		if j.Image == "" {
			return Config{}, fmt.Errorf("job %q: image is required", name)
		}
		if len(j.Script) == 0 {
			return Config{}, fmt.Errorf("job %q: script is required", name)
		}
		if j.Stage == "" {
			j.Stage = defaultStage
		}
		if _, ok := stageIndex[j.Stage]; !ok {
			return Config{}, fmt.Errorf("job %q: unknown stage %q", name, j.Stage)
		}
		cfg.Jobs = append(cfg.Jobs, Job{
			Name:      name,
			Stage:     j.Stage,
			Image:     j.Image,
			Script:    j.Script,
			Artifacts: j.Artifacts,
		})
	}
	sort.SliceStable(cfg.Jobs, func(i, j int) bool {
		si, sj := stageIndex[cfg.Jobs[i].Stage], stageIndex[cfg.Jobs[j].Stage]
		if si != sj {
			return si < sj
		}
		return cfg.Jobs[i].Name < cfg.Jobs[j].Name
	})
	if len(cfg.Jobs) == 0 {
		return Config{}, fmt.Errorf("pipeline has no jobs")
	}
	return cfg, nil
}

// Load reads the pipeline config from dir, preferring .knotwork-ci.yml.
func Load(dir string) (Config, string, error) {
	for _, name := range []string{ConfigFile, legacyConfigFile} {
		body, err := os.ReadFile(filepath.Join(dir, name))
		if err == nil {
			cfg, err := Parse(body)
			return cfg, name, err
		}
		if !errors.Is(err, os.ErrNotExist) {
			return Config{}, "", err
		}
	}
	return Config{}, "", ErrNoConfig
}
