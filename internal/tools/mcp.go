package tools

import (
	"context"
	"fmt"

	mcpp "github.com/cloudwego/eino-ext/components/tool/mcp"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cnjack/coding/internal/config"
	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
)

type MCPStatus struct {
	Name      string
	ToolCount int
	Error     error
	Running   bool
}

// LoadMCPTools establishes connections to configured MCP servers and fetches their tools.
func LoadMCPTools(ctx context.Context, mcpConfig map[string]*config.MCPServer) ([]tool.BaseTool, []MCPStatus) {
	var allTools []tool.BaseTool
	var statuses []MCPStatus

	for name, srv := range mcpConfig {
		if srv == nil {
			continue
		}

		status := MCPStatus{Name: name, Running: false}

		var cli *client.Client
		var err error

		if srv.Type == "http" {
			var opts []transport.StreamableHTTPCOption
			if len(srv.Headers) > 0 {
				opts = append(opts, transport.WithHTTPHeaders(srv.Headers))
			}
			cli, err = client.NewStreamableHttpClient(srv.URL, opts...)
		} else if srv.URL != "" || srv.Type == "sse" {
			var opts []transport.ClientOption
			if len(srv.Headers) > 0 {
				opts = append(opts, transport.WithHeaders(srv.Headers))
			}
			cli, err = client.NewSSEMCPClient(srv.URL, opts...)
		} else if srv.Command != "" || srv.Type == "stdio" {
			cli, err = client.NewStdioMCPClient(srv.Command, srv.Env, srv.Args...)
		} else {
			status.Error = fmt.Errorf("invalid config: missing url or command")
			statuses = append(statuses, status)
			continue
		}

		if err != nil {
			status.Error = fmt.Errorf("client create failed: %w", err)
			statuses = append(statuses, status)
			continue
		}

		if err := cli.Start(ctx); err != nil {
			status.Error = fmt.Errorf("start failed: %w", err)
			statuses = append(statuses, status)
			continue
		}

		initReq := mcp.InitializeRequest{}
		initReq.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
		initReq.Params.ClientInfo = mcp.Implementation{
			Name:    "little-jack",
			Version: "1.0.0",
		}

		if _, err := cli.Initialize(ctx, initReq); err != nil {
			status.Error = fmt.Errorf("init failed: %w", err)
			statuses = append(statuses, status)
			continue
		}

		// fetch tools
		ts, err := mcpp.GetTools(ctx, &mcpp.Config{Cli: cli})
		if err != nil {
			status.Error = fmt.Errorf("fetch tools failed: %w", err)
			statuses = append(statuses, status)
			continue
		}

		status.Running = true
		status.ToolCount = len(ts)
		statuses = append(statuses, status)

		allTools = append(allTools, ts...)
	}

	return allTools, statuses
}
