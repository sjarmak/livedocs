// Package config defines the .livedocs.yaml configuration format and provides
// loading with sane defaults. An empty YAML file is valid and uses all defaults
// (progressive disclosure).
package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

const (
	// DefaultConfigFile is the default configuration file name.
	DefaultConfigFile = ".livedocs.yaml"

	// DefaultDir is the default directory for livedocs data.
	DefaultDir = ".livedocs"

	// DefaultClaimsDBPath is the default path for the claims database,
	// relative to the repo root.
	DefaultClaimsDBPath = ".livedocs/claims.db"

	// DefaultCacheDBPath is the default path for the cache database,
	// relative to the repo root.
	DefaultCacheDBPath = ".livedocs/cache.db"

	// DefaultCacheCapBytes is the default cache capacity: 2 GB.
	DefaultCacheCapBytes int64 = 2 * 1024 * 1024 * 1024

	// DefaultTribalDailyBudget is the default daily LLM call budget for tribal extraction.
	DefaultTribalDailyBudget = 100

	// DefaultTribalModel is the default model for LLM-classified tribal extraction.
	DefaultTribalModel = "claude-haiku-4-5-20251001"

	// DefaultTribalMaxFilesPerRun is the default cap on files processed by the
	// tribal mining loop per invocation.
	DefaultTribalMaxFilesPerRun = 100

	// DefaultTribalCriticBudgetPercent is the default reserved share of the
	// tribal LLM budget allocated to a future critic loop.
	DefaultTribalCriticBudgetPercent = 20
)

// TribalConfig holds settings for LLM-classified tribal knowledge extraction.
// LLM extraction is opt-in: LLMEnabled must be true and --tribal=llm must be
// passed at the CLI. Deterministic extractors run regardless of this config.
type TribalConfig struct {
	// LLMEnabled gates LLM-classified tribal extraction. Default: false.
	LLMEnabled bool `yaml:"llm_enabled,omitempty"`

	// AllowedRepos restricts LLM extraction to these repositories.
	// Empty means all repositories are allowed when LLMEnabled is true.
	AllowedRepos []string `yaml:"allowed_repos,omitempty"`

	// DailyBudget is the maximum number of LLM calls per day.
	// Default: 100.
	DailyBudget int `yaml:"daily_budget,omitempty"`

	// Model is the LLM model identifier used for tribal extraction.
	// Default: claude-haiku-4-5-20251001.
	Model string `yaml:"model,omitempty"`

	// MaxFilesPerRun caps how many files the tribal mining loop processes per
	// invocation. Must be greater than zero. Default: 100.
	MaxFilesPerRun int `yaml:"max_files_per_run,omitempty"`

	// CriticBudgetPercent is the reserved share (0-100) of the tribal LLM
	// budget allocated to a future critic loop. Default: 20.
	CriticBudgetPercent int `yaml:"critic_budget_percent,omitempty"`

	// ClusterDebugEnabled toggles the cluster-debug sidecar DB used for
	// Phase 5 calibration. Default: false.
	ClusterDebugEnabled bool `yaml:"cluster_debug_enabled,omitempty"`
}

// Config represents the .livedocs.yaml configuration file.
// All fields are optional; zero values are replaced with sane defaults.
type Config struct {
	// Languages to scan. Empty means auto-detect from repository files.
	Languages []string `yaml:"languages,omitempty"`

	// Include patterns (glob). Empty means include all files.
	Include []string `yaml:"include,omitempty"`

	// Exclude patterns (glob). Defaults are always applied in addition.
	Exclude []string `yaml:"exclude,omitempty"`

	// ClaimsDB is the path to the claims database, relative to repo root.
	// Default: .livedocs/claims.db
	ClaimsDB string `yaml:"claims_db,omitempty"`

	// CacheDB is the path to the cache database, relative to repo root.
	// Default: .livedocs/cache.db
	CacheDB string `yaml:"cache_db,omitempty"`

	// Repo is the repository identifier (e.g., "kubernetes/kubernetes").
	// Default: auto-detected from git remote or directory name.
	Repo string `yaml:"repo,omitempty"`

	// Tribal holds LLM tribal extraction settings.
	Tribal TribalConfig `yaml:"tribal,omitempty"`
}

// defaultExclude are patterns always excluded during scanning.
var defaultExclude = []string{
	".git",
	"vendor",
	"node_modules",
	"_output",
	".livedocs",
	"testdata",
}

// ApplyDefaults fills in zero-value fields with sane defaults.
// It returns a new Config without mutating the receiver.
func (c Config) ApplyDefaults() Config {
	out := c
	if out.ClaimsDB == "" {
		out.ClaimsDB = DefaultClaimsDBPath
	}
	if out.CacheDB == "" {
		out.CacheDB = DefaultCacheDBPath
	}
	if out.Tribal.DailyBudget == 0 {
		out.Tribal.DailyBudget = DefaultTribalDailyBudget
	}
	if out.Tribal.Model == "" {
		out.Tribal.Model = DefaultTribalModel
	}
	if out.Tribal.MaxFilesPerRun == 0 {
		out.Tribal.MaxFilesPerRun = DefaultTribalMaxFilesPerRun
	}
	if out.Tribal.CriticBudgetPercent == 0 {
		out.Tribal.CriticBudgetPercent = DefaultTribalCriticBudgetPercent
	}
	// Merge default excludes with user excludes, avoiding duplicates.
	seen := make(map[string]bool, len(out.Exclude))
	for _, e := range out.Exclude {
		seen[e] = true
	}
	for _, d := range defaultExclude {
		if !seen[d] {
			out.Exclude = append(out.Exclude, d)
		}
	}
	return out
}

// Load reads and parses a .livedocs.yaml file, applying defaults.
// If the file does not exist, it returns a Config with all defaults.
// If the file exists but is empty, it also returns all defaults.
func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Config{}.ApplyDefaults(), nil
		}
		return Config{}, fmt.Errorf("config: read %s: %w", path, err)
	}
	return Parse(data)
}

// Parse decodes YAML bytes into a Config and applies defaults.
func Parse(data []byte) (Config, error) {
	var cfg Config
	if len(data) == 0 {
		return cfg.ApplyDefaults(), nil
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("config: parse yaml: %w", err)
	}
	if err := validate(&cfg); err != nil {
		return Config{}, fmt.Errorf("config: validate: %w", err)
	}
	return cfg.ApplyDefaults(), nil
}

// validate checks semantic constraints on parsed config values that cannot
// be expressed in the struct tags alone.
func validate(cfg *Config) error {
	if cfg.Tribal.MaxFilesPerRun < 0 {
		return fmt.Errorf("tribal.max_files_per_run must be > 0, got %d", cfg.Tribal.MaxFilesPerRun)
	}
	if cfg.Tribal.CriticBudgetPercent < 0 || cfg.Tribal.CriticBudgetPercent > 100 {
		return fmt.Errorf("tribal.critic_budget_percent must be in [0,100], got %d", cfg.Tribal.CriticBudgetPercent)
	}
	return nil
}

// Marshal serializes a Config to YAML bytes.
func Marshal(cfg Config) ([]byte, error) {
	return yaml.Marshal(cfg)
}

// DefaultYAML returns the default .livedocs.yaml content as a commented template.
func DefaultYAML(languages []string) string {
	cfg := Config{
		Languages: languages,
	}
	data, _ := yaml.Marshal(cfg)
	header := "# livedocs configuration\n# All fields are optional. An empty file uses sane defaults.\n# See: https://github.com/sjarmak/livedocs\n"
	tribalDoc := "\n# tribal:\n" +
		"#   # Opt in to LLM-classified tribal extraction (deterministic extractors\n" +
		"#   # always run regardless of this flag).\n" +
		"#   llm_enabled: false\n" +
		"#   # Max files processed by the tribal mining loop per invocation.\n" +
		fmt.Sprintf("#   max_files_per_run: %d\n", DefaultTribalMaxFilesPerRun) +
		"#   # Reserved share (0-100) of the tribal LLM budget for the critic loop.\n" +
		fmt.Sprintf("#   critic_budget_percent: %d\n", DefaultTribalCriticBudgetPercent) +
		"#   # Toggle the cluster-debug sidecar DB used for Phase 5 calibration.\n" +
		"#   cluster_debug_enabled: false\n"
	return header + string(data) + tribalDoc
}

// ConfigPath returns the absolute path to the config file for a given repo root.
func ConfigPath(repoRoot string) string {
	return filepath.Join(repoRoot, DefaultConfigFile)
}

// DirPath returns the absolute path to the .livedocs directory for a given repo root.
func DirPath(repoRoot string) string {
	return filepath.Join(repoRoot, DefaultDir)
}
