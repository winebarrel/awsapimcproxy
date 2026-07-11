package awsapimcproxy

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	appName = "awsapimcproxy"
)

// Proxy is a multi-profile MCP proxy in front of the AWS API MCP server.
//
// It launches one upstream AWS API MCP server process per AWS profile, exposes
// the upstream tools over stdio, and injects a required "profile" argument into
// each tool. On a tool call the profile selects the matching upstream process,
// and the call is forwarded to it.
type Proxy struct {
	config  *Config
	version string

	// baseCtx bounds the lifetime of the upstream subprocesses. It is the Run
	// context, so subprocesses live until the proxy stops -- not until the tool
	// call that first launched them returns.
	baseCtx context.Context

	mu       sync.Mutex
	sessions map[string]*mcp.ClientSession
}

// NewProxy creates a Proxy from the given config.
func NewProxy(config *Config, version string) *Proxy {
	return &Proxy{
		config:   config,
		version:  version,
		sessions: map[string]*mcp.ClientSession{},
	}
}

// Run builds the proxy server and serves it over stdio until the client
// disconnects or ctx is cancelled.
func (proxy *Proxy) Run(ctx context.Context) error {
	// Bind the upstream subprocesses to the proxy's lifetime, and close them when
	// the proxy stops (client disconnect or ctx cancellation).
	proxy.baseCtx = ctx
	defer proxy.closeSessions()

	server, err := proxy.buildServer(ctx)

	if err != nil {
		return err
	}

	return server.Run(ctx, &mcp.StdioTransport{})
}

// buildServer launches the upstream AWS API MCP server for the first profile,
// mirrors its tools (each with an injected profile argument, plus a
// proxy-native list_profiles tool) and returns a server ready to serve. It does
// not start serving.
func (proxy *Proxy) buildServer(ctx context.Context) (*mcp.Server, error) {
	if proxy.config == nil {
		return nil, fmt.Errorf("no profiles are configured")
	}

	// Validate here too so a programmatically constructed config (one that did
	// not go through LoadConfig) is rejected -- and DefaultCommand applied --
	// before any profile entry is dereferenced.
	if err := proxy.config.validate(); err != nil {
		return nil, err
	}

	server := mcp.NewServer(&mcp.Implementation{
		Name:    appName,
		Version: proxy.version,
	}, nil)

	// Discover the upstream tools using the first configured profile. Every
	// profile is assumed to expose the same set of AWS API tools.
	primary := proxy.config.Profiles[0].Name
	session, err := proxy.session(ctx, primary)

	if err != nil {
		return nil, fmt.Errorf("failed to start the upstream server for profile '%s': %w", primary, err)
	}

	tools, err := listTools(ctx, session)

	if err != nil {
		return nil, fmt.Errorf("failed to list upstream tools: %w", err)
	}

	profileNames := proxy.config.ProfileNames()

	// Add a proxy-native tool so clients can discover the configured profiles.
	server.AddTool(proxy.listProfilesTool())

	for _, tool := range tools {
		wrapped, handler, err := proxy.wrapTool(tool, profileNames)

		if err != nil {
			return nil, fmt.Errorf("failed to wrap tool '%s': %w", tool.Name, err)
		}

		server.AddTool(wrapped, handler)
	}

	log.Printf("[%s] serving %d AWS API tools for %d profiles: %v", appName, len(tools), len(profileNames), profileNames)

	return server, nil
}

// session returns a connected upstream session for the profile, launching and
// caching its subprocess on first use.
//
// The upstream process is started without holding proxy.mu so that a slow start
// does not block tool calls for other profiles. If two callers race to start
// the same profile, both launch but only the first published session is kept;
// the loser's session (and its subprocess) is closed.
func (proxy *Proxy) session(ctx context.Context, profile string) (*mcp.ClientSession, error) {
	proxy.mu.Lock()
	cached, ok := proxy.sessions[profile]
	proxy.mu.Unlock()

	if ok {
		return cached, nil
	}

	profileConfig := proxy.config.Profile(profile)

	if profileConfig == nil {
		return nil, fmt.Errorf("unknown profile: %s", profile)
	}

	client := mcp.NewClient(&mcp.Implementation{
		Name:    appName,
		Version: proxy.version,
	}, nil)

	transport := &mcp.CommandTransport{Command: proxy.command(profileConfig)}

	session, err := client.Connect(ctx, transport, nil)

	if err != nil {
		return nil, err
	}

	proxy.mu.Lock()
	// Another goroutine may have started the same profile while we were connecting.
	if existing, ok := proxy.sessions[profile]; ok {
		proxy.mu.Unlock()
		_ = session.Close()

		return existing, nil
	}

	proxy.sessions[profile] = session
	proxy.mu.Unlock()

	return session, nil
}

// command builds the exec.Cmd that launches the upstream AWS API MCP server for
// the profile, injecting the AWS profile and region into its environment.
//
// The command is bound to proxy.baseCtx (the proxy's lifetime), not a per-call
// context, so a cached subprocess survives the tool call that launched it.
func (proxy *Proxy) command(profile *ProfileConfig) *exec.Cmd {
	argv := proxy.config.Command

	if len(argv) == 0 {
		argv = DefaultCommand
	}

	ctx := proxy.baseCtx

	if ctx == nil {
		ctx = context.Background()
	}

	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)

	// Merge into a map first so our overrides win over any duplicate keys already
	// present in the inherited environment (duplicate env entries are not
	// well-defined across platforms).
	env := map[string]string{}

	for _, kv := range os.Environ() {
		if i := strings.IndexByte(kv, '='); i >= 0 {
			env[kv[:i]] = kv[i+1:]
		}
	}

	if profile.AWSProfile != "" {
		env["AWS_API_MCP_PROFILE_NAME"] = profile.AWSProfile
	}

	if profile.Region != "" {
		env["AWS_REGION"] = profile.Region
	}

	for k, v := range profile.Env {
		env[k] = v
	}

	cmd.Env = make([]string, 0, len(env))

	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}

	// The upstream server logs to stderr; stdout carries the MCP stdio protocol.
	cmd.Stderr = os.Stderr

	return cmd
}

// listTools collects every tool from the upstream server, following pagination.
func listTools(ctx context.Context, session *mcp.ClientSession) ([]*mcp.Tool, error) {
	var tools []*mcp.Tool

	params := &mcp.ListToolsParams{}

	for {
		result, err := session.ListTools(ctx, params)

		if err != nil {
			return nil, err
		}

		tools = append(tools, result.Tools...)

		if result.NextCursor == "" {
			break
		}

		params.Cursor = result.NextCursor
	}

	return tools, nil
}
