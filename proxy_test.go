package awsapimcproxy

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeServerEnv marks a re-exec of the test binary that should behave as a fake
// upstream AWS API MCP server rather than run the tests.
const fakeServerEnv = "AWSAPIMCPROXY_FAKE_SERVER"

// TestMain lets the test binary double as a fake upstream MCP server: the proxy
// launches this same binary as a subprocess (with fakeServerEnv set) instead of
// the real aws-api-mcp-server.
func TestMain(m *testing.M) {
	if os.Getenv(fakeServerEnv) == "1" {
		runFakeServer()
		return
	}

	os.Exit(m.Run())
}

// runFakeServer serves a minimal MCP server over stdio exposing a "whoami" tool
// that echoes the AWS environment the proxy injected, so tests can assert the
// per-profile environment reached the subprocess.
func runFakeServer() {
	server := mcp.NewServer(&mcp.Implementation{Name: "fake-aws-api", Version: "0"}, nil)

	server.AddTool(
		&mcp.Tool{Name: "whoami", InputSchema: map[string]any{"type": "object"}},
		func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			text := "profile=" + os.Getenv("AWS_API_MCP_PROFILE_NAME") + " region=" + os.Getenv("AWS_REGION")
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}, nil
		},
	)

	_ = server.Run(context.Background(), &mcp.StdioTransport{})
}

// fakeConfig builds a config whose upstream command re-execs this test binary
// as the fake server.
func fakeConfig(profiles ...*ProfileConfig) *Config {
	for _, p := range profiles {
		if p.Env == nil {
			p.Env = map[string]string{}
		}
		p.Env[fakeServerEnv] = "1"
	}

	return &Config{Command: []string{os.Args[0]}, Profiles: profiles}
}

// TestLive runs against the real aws-api-mcp-server via uvx. It is gated on
// AWSAPIMCPROXY_LIVE=1 because it needs uv installed and network access.
func TestLive(t *testing.T) {
	if os.Getenv("AWSAPIMCPROXY_LIVE") != "1" {
		t.Skip("set AWSAPIMCPROXY_LIVE=1 to run against the real aws-api-mcp-server")
	}

	proxy := NewProxy(&Config{Profiles: []*ProfileConfig{{Name: "default"}}}, "test")
	defer proxy.closeSessions()

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	session, err := proxy.session(ctx, "default")
	require.NoError(t, err)

	tools, err := listTools(ctx, session)
	require.NoError(t, err)

	names := make([]string, len(tools))
	for i, tool := range tools {
		names[i] = tool.Name
	}

	assert.Contains(t, names, "call_aws")
}

func TestCommand(t *testing.T) {
	// A conflicting value in the inherited environment must be overridden, not
	// duplicated.
	t.Setenv("AWS_REGION", "inherited-region")

	proxy := NewProxy(&Config{Command: []string{"my-cmd", "--flag"}}, "test")

	cmd := proxy.command(&ProfileConfig{
		Name:       "dev",
		AWSProfile: "my-dev",
		Region:     "us-east-1",
		Env:        map[string]string{"READ_OPERATIONS_ONLY": "true"},
	})

	assert.Equal(t, []string{"my-cmd", "--flag"}, cmd.Args)
	assert.Contains(t, cmd.Env, "AWS_API_MCP_PROFILE_NAME=my-dev")
	assert.Contains(t, cmd.Env, "AWS_REGION=us-east-1")
	assert.Contains(t, cmd.Env, "READ_OPERATIONS_ONLY=true")

	// The override wins and is not duplicated.
	assert.NotContains(t, cmd.Env, "AWS_REGION=inherited-region")

	regions := 0
	for _, kv := range cmd.Env {
		if strings.HasPrefix(kv, "AWS_REGION=") {
			regions++
		}
	}
	assert.Equal(t, 1, regions, "AWS_REGION should appear exactly once")
}

func TestSessionSurvivesRequestContextCancel(t *testing.T) {
	proxy := NewProxy(fakeConfig(
		&ProfileConfig{Name: "dev", AWSProfile: "my-dev"},
	), "test")
	defer proxy.closeSessions()

	// Launch the subprocess with a cancelable per-call context, then cancel it.
	ctx, cancel := context.WithCancel(context.Background())

	session, err := proxy.session(ctx, "dev")
	require.NoError(t, err)

	cancel()

	// The cached subprocess must still be usable after the call's context ends.
	_, err = session.ListTools(context.Background(), &mcp.ListToolsParams{})
	assert.NoError(t, err)
}

func TestSession(t *testing.T) {
	proxy := NewProxy(fakeConfig(
		&ProfileConfig{Name: "dev", AWSProfile: "my-dev", Region: "us-east-1"},
	), "test")
	defer proxy.closeSessions()

	ctx := context.Background()

	dev1, err := proxy.session(ctx, "dev")
	require.NoError(t, err)

	// The same profile returns the cached session.
	dev2, err := proxy.session(ctx, "dev")
	require.NoError(t, err)
	assert.Same(t, dev1, dev2, "session was not cached")

	// An unknown profile is an error.
	_, err = proxy.session(ctx, "nope")
	assert.Error(t, err)

	// dropSession terminates the subprocess and forces a reconnect.
	proxy.dropSession("dev")

	dev3, err := proxy.session(ctx, "dev")
	require.NoError(t, err)
	assert.NotSame(t, dev1, dev3, "dropSession did not force a reconnect")
}

func TestSessionStartError(t *testing.T) {
	proxy := NewProxy(&Config{
		Command:  []string{"/nonexistent/awsapimcproxy-test-binary"},
		Profiles: []*ProfileConfig{{Name: "dev"}},
	}, "test")

	_, err := proxy.session(context.Background(), "dev")
	assert.Error(t, err)
}

func TestSessionConcurrent(t *testing.T) {
	proxy := NewProxy(fakeConfig(
		&ProfileConfig{Name: "dev", AWSProfile: "my-dev"},
	), "test")
	defer proxy.closeSessions()

	const n = 8

	var wg sync.WaitGroup

	sessions := make([]*mcp.ClientSession, n)
	errs := make([]error, n)
	start := make(chan struct{})

	for i := range n {
		wg.Add(1)

		go func(i int) {
			defer wg.Done()
			<-start
			sessions[i], errs[i] = proxy.session(context.Background(), "dev")
		}(i)
	}

	close(start)
	wg.Wait()

	for i := range n {
		require.NoError(t, errs[i])
		assert.Same(t, sessions[0], sessions[i], "caching is not race-safe")
	}
}

func TestWrapTool(t *testing.T) {
	proxy := NewProxy(fakeConfig(
		&ProfileConfig{Name: "dev", AWSProfile: "my-dev", Region: "us-east-1"},
		&ProfileConfig{Name: "prod", AWSProfile: "my-prod", Region: "ap-northeast-1"},
	), "test")
	defer proxy.closeSessions()

	profileNames := []string{"dev", "prod"}

	wrapped, handler, err := proxy.wrapTool(
		&mcp.Tool{Name: "whoami", InputSchema: map[string]any{"type": "object"}},
		profileNames,
	)
	require.NoError(t, err)

	props := wrapped.InputSchema.(map[string]any)["properties"].(map[string]any)
	assert.Contains(t, props, profileArg)

	call := func(args string) *mcp.CallToolResult {
		t.Helper()
		res, err := handler(context.Background(), &mcp.CallToolRequest{
			Params: &mcp.CallToolParamsRaw{Arguments: json.RawMessage(args)},
		})
		require.NoError(t, err)

		return res
	}

	// The call is forwarded to the selected profile's process, which sees that
	// profile's injected AWS environment.
	res := call(`{"profile":"prod"}`)
	require.False(t, res.IsError, "unexpected error: %+v", res.Content)
	assert.Equal(t, "profile=my-prod region=ap-northeast-1", res.Content[0].(*mcp.TextContent).Text)

	// Error paths.
	assert.True(t, call(`{}`).IsError, "expected an error when profile is missing")
	assert.True(t, call(`{"profile":"nope"}`).IsError, "expected an error for an unknown profile")
	assert.True(t, call(`{bad`).IsError, "expected an error for malformed arguments")
}

func TestBuildServer(t *testing.T) {
	proxy := NewProxy(fakeConfig(
		&ProfileConfig{Name: "dev", AWSProfile: "my-dev"},
	), "test")
	defer proxy.closeSessions()

	server, err := proxy.buildServer(context.Background())
	require.NoError(t, err)

	clientTransport, serverTransport := mcp.NewInMemoryTransports()

	serverSession, err := server.Connect(context.Background(), serverTransport, nil)
	require.NoError(t, err)
	defer func() { assert.NoError(t, serverSession.Close()) }()

	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)

	clientSession, err := client.Connect(context.Background(), clientTransport, nil)
	require.NoError(t, err)
	defer func() { assert.NoError(t, clientSession.Close()) }()

	tools, err := listTools(context.Background(), clientSession)
	require.NoError(t, err)

	byName := map[string]*mcp.Tool{}
	for _, tool := range tools {
		byName[tool.Name] = tool
	}

	assert.Contains(t, byName, "list_profiles")

	whoami, ok := byName["whoami"]
	require.True(t, ok, "upstream whoami tool was not mirrored")

	schema, err := json.Marshal(whoami.InputSchema)
	require.NoError(t, err)
	assert.Contains(t, string(schema), `"`+profileArg+`"`)
}

func TestRunErrors(t *testing.T) {
	ctx := context.Background()

	assert.Error(t, NewProxy(nil, "test").Run(ctx))
	assert.Error(t, NewProxy(&Config{}, "test").Run(ctx))

	proxy := NewProxy(&Config{
		Command:  []string{"/nonexistent/awsapimcproxy-test-binary"},
		Profiles: []*ProfileConfig{{Name: "dev"}},
	}, "test")

	assert.Error(t, proxy.Run(ctx))
}

func TestCloseSessions(t *testing.T) {
	proxy := NewProxy(fakeConfig(
		&ProfileConfig{Name: "dev", AWSProfile: "my-dev"},
	), "test")

	_, err := proxy.session(context.Background(), "dev")
	require.NoError(t, err)

	proxy.closeSessions()

	proxy.mu.Lock()
	n := len(proxy.sessions)
	proxy.mu.Unlock()

	assert.Zero(t, n)
}
