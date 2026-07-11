package awsapimcproxy

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestListProfilesTool(t *testing.T) {
	proxy := NewProxy(&Config{Profiles: []*ProfileConfig{
		{Name: "dev", AWSProfile: "my-dev", Region: "us-east-1"},
		{Name: "prod", AWSProfile: "my-prod", Region: "ap-northeast-1"},
	}}, "test")

	tool, handler := proxy.listProfilesTool()

	assert.Equal(t, "list_profiles", tool.Name)

	res, err := handler(context.Background(), &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{}})
	require.NoError(t, err)

	body := res.Content[0].(*mcp.TextContent).Text

	for _, want := range []string{"dev", "prod", "my-dev", "us-east-1"} {
		assert.Contains(t, body, want)
	}
}

func TestInjectProfileArg(t *testing.T) {
	upstream := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"cli_command": map[string]any{"type": "string"},
		},
		"required": []any{"cli_command"},
	}

	schema, err := injectProfileArg(upstream, []string{"dev", "prod"})
	require.NoError(t, err)

	props := schema["properties"].(map[string]any)
	assert.Contains(t, props, "cli_command", "original property was dropped")

	profile, ok := props[profileArg].(map[string]any)
	require.True(t, ok, "profile property was not added")

	assert.Equal(t, "string", profile["type"])
	assert.Equal(t, []any{"dev", "prod"}, profile["enum"])
	assert.Equal(t, []any{profileArg, "cli_command"}, schema["required"])

	// The upstream schema must not be mutated.
	assert.NotContains(t, upstream["properties"].(map[string]any), profileArg)
}

func TestInjectProfileArgEmptySchema(t *testing.T) {
	schema, err := injectProfileArg(nil, []string{"dev"})
	require.NoError(t, err)

	assert.Equal(t, "object", schema["type"])
	assert.Equal(t, []any{profileArg}, schema["required"])
}

func TestInjectProfileArgRawMessage(t *testing.T) {
	raw := json.RawMessage(`{"type":"object","properties":{"q":{"type":"string"}}}`)

	schema, err := injectProfileArg(raw, []string{"dev"})
	require.NoError(t, err)

	props := schema["properties"].(map[string]any)
	assert.Contains(t, props, profileArg)
	assert.Contains(t, props, "q")
}

func TestInjectProfileArgNonObject(t *testing.T) {
	_, err := injectProfileArg([]any{"not", "an", "object"}, []string{"dev"})
	assert.Error(t, err)
}
