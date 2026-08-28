package agentruntime

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	ErrInvalidSubagentConfig = errors.New("invalid subagent configuration")
	ErrSubagentNotFound      = errors.New("subagent configuration not found")
)

// SubagentConfig describes one named child role. The registry is deliberately
// data-only: it controls isolation and budgets, while the parent still owns
// task creation and the durable Worker lifecycle.
type SubagentConfig struct {
	Name            string
	SystemPrompt    string
	Tools           []string
	DisallowedTools []string
	Skills          []string
	Model           string
	MaxSteps        int
	Timeout         time.Duration
}

func (c SubagentConfig) AllowsTool(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" || name == "delegate_research" || name == "task" || name == "create_subagent" {
		return false
	}
	for _, denied := range c.DisallowedTools {
		if strings.TrimSpace(denied) == name {
			return false
		}
	}
	if len(c.Tools) == 0 {
		return true
	}
	for _, allowed := range c.Tools {
		if strings.TrimSpace(allowed) == name {
			return true
		}
	}
	return false
}

type SubagentRegistry struct {
	configs map[string]SubagentConfig
}

func NewSubagentRegistry(configs []SubagentConfig) (*SubagentRegistry, error) {
	registry := &SubagentRegistry{configs: make(map[string]SubagentConfig, len(configs))}
	for _, config := range configs {
		if err := registry.Register(config); err != nil {
			return nil, err
		}
	}
	return registry, nil
}

func (r *SubagentRegistry) Register(config SubagentConfig) error {
	if r == nil {
		return ErrInvalidSubagentConfig
	}
	config.Name = strings.TrimSpace(config.Name)
	if config.Name == "" || config.MaxSteps <= 0 || config.Timeout <= 0 {
		return ErrInvalidSubagentConfig
	}
	if config.Name == "delegate_research" || config.Name == "task" || config.Name == "create_subagent" {
		return fmt.Errorf("%w: reserved name %q", ErrInvalidSubagentConfig, config.Name)
	}
	if r.configs == nil {
		r.configs = make(map[string]SubagentConfig)
	}
	config.Tools = cloneStrings(config.Tools)
	config.DisallowedTools = cloneStrings(config.DisallowedTools)
	config.Skills = cloneStrings(config.Skills)
	r.configs[config.Name] = config
	return nil
}

func (r *SubagentRegistry) Get(name string) (SubagentConfig, error) {
	if r == nil {
		return SubagentConfig{}, ErrSubagentNotFound
	}
	config, ok := r.configs[strings.TrimSpace(name)]
	if !ok {
		return SubagentConfig{}, ErrSubagentNotFound
	}
	config.Tools = cloneStrings(config.Tools)
	config.DisallowedTools = cloneStrings(config.DisallowedTools)
	config.Skills = cloneStrings(config.Skills)
	return config, nil
}

func (r *SubagentRegistry) List() []SubagentConfig {
	if r == nil {
		return nil
	}
	result := make([]SubagentConfig, 0, len(r.configs))
	for _, config := range r.configs {
		result = append(result, config)
	}
	return result
}

func cloneStrings(values []string) []string {
	return append([]string(nil), values...)
}
