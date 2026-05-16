package config

import (
	"errors"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

type Project struct {
	ID     string   `yaml:"id"`
	Groups []string `yaml:"groups"`
}

type Config struct {
	GoogleDomain string    `yaml:"google_domain"`
	ManagedGroup string    `yaml:"managed_group"`
	Projects     []Project `yaml:"projects"`
}

// Load reads, parses, and validates the YAML config at path.
func Load(path string) (*Config, error) {
	if path == "" {
		return nil, errors.New("config: path is empty")
	}

	f, err := os.Open(path) //nolint:gosec // config path from trusted CLI flag
	if err != nil {
		return nil, fmt.Errorf("config: open %q: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	var cfg Config
	dec := yaml.NewDecoder(f)
	// KnownFields surfaces typos in YAML keys instead of silently ignoring them.
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("config: parse %q: %w", path, err)
	}

	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("config: validate %q: %w", path, err)
	}
	return &cfg, nil
}

func (c *Config) validate() error {
	if c.GoogleDomain == "" {
		return errors.New("google_domain is required")
	}
	if c.ManagedGroup == "" {
		return errors.New("managed_group is required")
	}
	if len(c.Projects) == 0 {
		return errors.New("projects must contain at least one entry")
	}
	seen := make(map[string]struct{}, len(c.Projects))
	for i, p := range c.Projects {
		if p.ID == "" {
			return fmt.Errorf("projects[%d].id is required", i)
		}
		if _, dup := seen[p.ID]; dup {
			return fmt.Errorf("projects[%d].id %q is duplicated", i, p.ID)
		}
		seen[p.ID] = struct{}{}
		if len(p.Groups) == 0 {
			return fmt.Errorf("projects[%d].groups must contain at least one entry", i)
		}
		for j, g := range p.Groups {
			if g == "" {
				return fmt.Errorf("projects[%d].groups[%d] is empty", i, j)
			}
		}
	}
	return nil
}
