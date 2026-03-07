package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

const (
	configDir  = ".jcoding"
	configFile = "config.json"
)

type ProviderConfig struct {
	APIKey  string   `json:"api_key"`
	BaseURL string   `json:"base_url,omitempty"`
	Models  []string `json:"models"`
}

// SSHAlias represents a saved SSH connection alias
type SSHAlias struct {
	Name string `json:"name"`
	Addr string `json:"addr"` // user@host
	Path string `json:"path,omitempty"` // remote working directory
}

// MCPServer represents a configured MCP server connection
type MCPServer struct {
	Type    string            `json:"type,omitempty"`
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     []string          `json:"env,omitempty"`
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}

// Config represents the application configuration
type Config struct {
	Models        map[string]*ProviderConfig `json:"models"`
	Provider      string                     `json:"provider"`
	Model         string                     `json:"model"`
	MaxIterations int                        `json:"max_iterations,omitempty"`
	SSHAliases    []SSHAlias                 `json:"ssh_aliases,omitempty"`
	MCPServers    map[string]*MCPServer      `json:"mcp_servers,omitempty"`
}

// configFilePath returns the full path to the config file
func configFilePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to get home directory: %w", err)
	}
	return filepath.Join(home, configDir, configFile), nil
}

// HistoryFilePath returns the full path to the history file
func HistoryFilePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to get home directory: %w", err)
	}
	return filepath.Join(home, configDir, "history"), nil
}

// NeedsSetup returns true if the config file does not exist or is incomplete.
func NeedsSetup() bool {
	cfgPath, err := configFilePath()
	if err != nil {
		return true
	}
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		return true
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return true
	}
	return len(cfg.Models) == 0
}

// LoadConfig loads configuration from $HOME/.jcoding/config.json.
func LoadConfig() (*Config, error) {
	cfg := &Config{
		MaxIterations: 1000, // default
	}

	cfgPath, err := configFilePath()
	if err != nil {
		return nil, fmt.Errorf("config file path error: %w", err)
	}

	data, err := os.ReadFile(cfgPath)
	if err != nil {
		return nil, fmt.Errorf("config file not found at %s, please run setup first", cfgPath)
	}

	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config file %s: %w", cfgPath, err)
	}

	// Validation
	if len(cfg.Models) == 0 {
		return nil, fmt.Errorf("no models configured: set 'models' in %s", cfgPath)
	}

	// Resolve default provider and model if not set
	if cfg.Provider == "" || cfg.Model == "" {
		for providerName, providerCfg := range cfg.Models {
			if len(providerCfg.Models) > 0 {
				cfg.Provider = providerName
				cfg.Model = providerCfg.Models[0]
				break
			}
		}
	}

	if cfg.MaxIterations <= 0 {
		cfg.MaxIterations = 1000
	}

	return cfg, nil
}

// SaveConfig writes the config to $HOME/.jcoding/config.json.
func SaveConfig(cfg *Config) error {
	cfgPath, err := configFilePath()
	if err != nil {
		return fmt.Errorf("config file path error: %w", err)
	}

	// Ensure directory exists
	dir := filepath.Dir(cfgPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create config directory %s: %w", dir, err)
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	if err := os.WriteFile(cfgPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write config file %s: %w", cfgPath, err)
	}

	return nil
}

// ConfigPath returns the expected path of the config file (for display purposes)
func ConfigPath() string {
	p, err := configFilePath()
	if err != nil {
		return filepath.Join("~", configDir, configFile)
	}
	return p
}
