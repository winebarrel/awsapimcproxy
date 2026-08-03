# awsapimcproxy

[![CI](https://github.com/winebarrel/awsapimcproxy/actions/workflows/ci.yml/badge.svg)](https://github.com/winebarrel/awsapimcproxy/actions/workflows/ci.yml)
[![codecov](https://codecov.io/gh/winebarrel/awsapimcproxy/branch/main/graph/badge.svg)](https://codecov.io/gh/winebarrel/awsapimcproxy)
[![AI Generated](https://img.shields.io/badge/AI%20Generated-Claude-orange?logo=anthropic)](https://claude.ai/claude-code)

A multi-profile proxy for the [AWS API MCP Server](https://github.com/awslabs/mcp/tree/main/src/aws-api-mcp-server).

The AWS API MCP server picks its AWS identity from the environment
(`AWS_API_MCP_PROFILE_NAME`) at launch, so a single server can only use one
profile. `awsapimcproxy` runs one upstream server per profile, mirrors all of
their tools, and adds a `profile` argument to each tool. When a tool is called,
the proxy picks the matching profile's process and forwards the request.

```
Claude Code ──stdio──▶ awsapimcproxy ──┬─ profile=dev  ─▶ aws-api-mcp-server (AWS_API_MCP_PROFILE_NAME=my-dev)
                                       └─ profile=prod ─▶ aws-api-mcp-server (AWS_API_MCP_PROFILE_NAME=my-prod)
```

## Requirements

The upstream server is launched on demand. By default that is
`uvx awslabs.aws-api-mcp-server@latest`, so [uv](https://docs.astral.sh/uv/)
must be installed (override with `command` in the config).

## Install

```
go install github.com/winebarrel/awsapimcproxy/cmd/awsapimcproxy@latest
```

## Configuration

Create a YAML config file. Values are passed through `os.ExpandEnv`, so values
can be referenced as `${ENV_VAR}` instead of being written literally.

```yaml
# awsapimcproxy.yml

# Optional. Default: [uvx, awslabs.aws-api-mcp-server@latest]
# command: [uvx, awslabs.aws-api-mcp-server@latest]

profiles:
  - name: dev
    aws_profile: my-dev
    region: us-east-1
  - name: prod
    aws_profile: my-prod
    region: ap-northeast-1
```

Each profile launches its own upstream process:

- `aws_profile` is passed as `AWS_API_MCP_PROFILE_NAME`.
- `region` is passed as `AWS_REGION`.
- `env` adds or overrides any other environment variables for that process.

## Usage

```
Usage: awsapimcproxy --config=STRING [flags]

Flags:
  -h, --help             Show help.
  -c, --config=STRING    Config file path ($AWSAPIMCPROXY_CONFIG).
      --version
```

### Claude Code

Register it as an MCP server:

```json
{
  "mcpServers": {
    "awsapimcproxy": {
      "command": "awsapimcproxy",
      "args": ["--config", "/path/to/awsapimcproxy.yml"]
    }
  }
}
```

## How it works

- On startup the proxy launches the upstream server for the first profile and
  lists its tools.
- Each upstream tool is re-registered with a required `profile` string argument
  (enumerated over the configured profile names). A proxy-native `list_profiles`
  tool is also added.
- On a tool call, the proxy strips the `profile` argument, looks up that
  profile's upstream process (launching it lazily, then caching it), and
  forwards the call.
