package awsapimcproxy

import (
	"fmt"
	"os"
	"slices"

	"gopkg.in/yaml.v3"
)

// DefaultCommand is the command used to launch an upstream AWS API MCP server
// when the config does not override it.
// See https://github.com/awslabs/mcp/tree/main/src/aws-api-mcp-server
var DefaultCommand = []string{"uvx", "awslabs.aws-api-mcp-server@latest"}

// ProfileConfig selects the AWS identity for one upstream AWS API MCP server.
//
// Each profile launches its own upstream process. AWSProfile is passed as
// AWS_API_MCP_PROFILE_NAME and Region as AWS_REGION; Env adds or overrides any
// other environment variables for that process.
type ProfileConfig struct {
	Name       string            `yaml:"name"`
	AWSProfile string            `yaml:"aws_profile,omitempty"`
	Region     string            `yaml:"region,omitempty"`
	Env        map[string]string `yaml:"env,omitempty"`
}

// Config is the awsapimcproxy configuration file.
type Config struct {
	// Command launches the upstream AWS API MCP server. Defaults to DefaultCommand.
	Command  []string         `yaml:"command,omitempty"`
	Profiles []*ProfileConfig `yaml:"profiles"`
}

// LoadConfig reads and validates the config file at path.
//
// The file content is passed through os.ExpandEnv so that values can be
// referenced as ${ENV_VAR} instead of being written literally.
func LoadConfig(path string) (*Config, error) {
	buf, err := os.ReadFile(path)

	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	var config Config

	if err := yaml.Unmarshal([]byte(os.ExpandEnv(string(buf))), &config); err != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}

	if err := config.validate(); err != nil {
		return nil, err
	}

	return &config, nil
}

func (config *Config) validate() error {
	if len(config.Command) == 0 {
		// Clone so callers cannot mutate the shared DefaultCommand slice.
		config.Command = slices.Clone(DefaultCommand)
	}

	if len(config.Profiles) == 0 {
		return fmt.Errorf("no profiles are configured")
	}

	seen := map[string]bool{}

	for i, profile := range config.Profiles {
		if profile == nil {
			return fmt.Errorf("profiles[%d]: is empty", i)
		}

		if profile.Name == "" {
			return fmt.Errorf("profiles[%d]: 'name' is required", i)
		}

		if seen[profile.Name] {
			return fmt.Errorf("profiles[%d]: duplicated profile name: %s", i, profile.Name)
		}

		seen[profile.Name] = true
	}

	return nil
}

// ProfileNames returns the configured profile names in file order.
func (config *Config) ProfileNames() []string {
	names := make([]string, len(config.Profiles))

	for i, profile := range config.Profiles {
		names[i] = profile.Name
	}

	return names
}

// Profile returns the config for the named profile, or nil if it is not configured.
func (config *Config) Profile(name string) *ProfileConfig {
	for _, profile := range config.Profiles {
		if profile.Name == name {
			return profile
		}
	}

	return nil
}
