package awsapimcproxy

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0600))

	return path
}

func TestLoadConfig(t *testing.T) {
	t.Setenv("TEST_REGION", "ap-northeast-1")

	path := writeConfig(t, `
command: [my-aws-mcp, --flag]
profiles:
  - name: dev
    aws_profile: my-dev
    region: us-east-1
  - name: prod
    aws_profile: my-prod
    region: ${TEST_REGION}
    env:
      READ_OPERATIONS_ONLY: "true"
`)

	config, err := LoadConfig(path)
	require.NoError(t, err)

	assert.Equal(t, []string{"my-aws-mcp", "--flag"}, config.Command)
	assert.Equal(t, []string{"dev", "prod"}, config.ProfileNames())
	assert.Equal(t, "my-dev", config.Profile("dev").AWSProfile)

	// Env expansion.
	assert.Equal(t, "ap-northeast-1", config.Profile("prod").Region)
	assert.Equal(t, "true", config.Profile("prod").Env["READ_OPERATIONS_ONLY"])

	assert.Nil(t, config.Profile("nope"))
}

func TestLoadConfigDefaultCommand(t *testing.T) {
	path := writeConfig(t, "profiles:\n  - name: dev\n")

	config, err := LoadConfig(path)
	require.NoError(t, err)

	assert.Equal(t, DefaultCommand, config.Command)
}

func TestLoadConfigFileErrors(t *testing.T) {
	_, err := LoadConfig(filepath.Join(t.TempDir(), "does-not-exist.yml"))
	assert.Error(t, err)

	_, err = LoadConfig(writeConfig(t, "profiles: [1, 2"))
	assert.Error(t, err)
}

func TestLoadConfigErrors(t *testing.T) {
	cases := map[string]string{
		"no profiles":  `command: [x]`,
		"missing name": "profiles:\n  - aws_profile: a\n",
		"duplicate":    "profiles:\n  - name: dev\n  - name: dev\n",
	}

	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := LoadConfig(writeConfig(t, content))
			assert.Error(t, err)
		})
	}
}
