/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package mcp

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/kubeswarm/kubeswarm/pkg/agent/config"
)

const contentTypeText = "text"

// defaultMCPClient is used for unauthenticated MCP servers.
// The 30s timeout is a transport-level safety net; the task context provides the primary bound.
var defaultMCPClient = &http.Client{Timeout: 30 * time.Second}

// serverConn holds the per-server http.Client and bearer token resolved at startup.
type serverConn struct {
	client      *http.Client
	bearerToken string // empty when not using bearer auth
}

// Tool is the internal representation of a tool discovered from an MCP server.
type Tool struct {
	// Name is prefixed: "<server_name>__<original_tool_name>"
	Name        string
	Description string
	InputSchema json.RawMessage
	ServerURL   string
	// OriginalName is the tool name as registered on the MCP server (without prefix).
	OriginalName string
}

// Manager connects to MCP servers and provides tool discovery and execution.
type Manager struct {
	tools       []Tool
	serverConns map[string]serverConn // keyed by server URL
}

// NewManager connects to all configured MCP servers, resolves auth credentials,
// and discovers their tools. Auth failures abort startup.
func NewManager(servers []config.MCPServerConfig) (*Manager, error) {
	m := &Manager{serverConns: make(map[string]serverConn, len(servers))}
	for _, s := range servers {
		conn, err := buildServerConn(s)
		if err != nil {
			return nil, fmt.Errorf("MCP server %q auth setup failed: %w", s.Name, err)
		}
		m.serverConns[s.URL] = conn

		tools, err := discoverTools(s, conn)
		if err != nil {
			return nil, fmt.Errorf("failed to discover tools from MCP server %q: %w", s.Name, err)
		}
		m.tools = append(m.tools, tools...)
	}
	return m, nil
}

// buildServerConn resolves auth credentials and returns a per-server connection config.
func buildServerConn(s config.MCPServerConfig) (serverConn, error) {
	switch s.AuthType {
	case "bearer":
		if s.TokenEnvVar == "" {
			return serverConn{}, fmt.Errorf("bearer auth configured but tokenEnvVar is empty")
		}
		token := os.Getenv(s.TokenEnvVar)
		if token == "" {
			return serverConn{}, fmt.Errorf("bearer token env var %q is not set or empty", s.TokenEnvVar)
		}
		return serverConn{client: defaultMCPClient, bearerToken: token}, nil

	case "mtls":
		if s.CertFile == "" || s.KeyFile == "" {
			return serverConn{}, fmt.Errorf("mtls auth configured but certFile or keyFile is empty")
		}
		cert, err := tls.LoadX509KeyPair(s.CertFile, s.KeyFile)
		if err != nil {
			return serverConn{}, fmt.Errorf("loading mTLS cert/key: %w", err)
		}
		tlsCfg := &tls.Config{
			Certificates: []tls.Certificate{cert},
			MinVersion:   tls.VersionTLS12,
		}
		c := &http.Client{
			Timeout:   30 * time.Second,
			Transport: &http.Transport{TLSClientConfig: tlsCfg},
		}
		return serverConn{client: c}, nil

	default:
		// "none" or unset — use the shared default client.
		return serverConn{client: defaultMCPClient}, nil
	}
}

// applyAuth sets the Authorization header on req when a bearer token is configured.
func (conn serverConn) applyAuth(req *http.Request) {
	if conn.bearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+conn.bearerToken)
	}
}

// discoverTools calls the MCP server's tools/list endpoint.
func discoverTools(server config.MCPServerConfig, conn serverConn) ([]Tool, error) {
	req, err := http.NewRequest(http.MethodPost, server.URL+"/tools/list", nil)
	if err != nil {
		return nil, fmt.Errorf("building tools/list request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	conn.applyAuth(req)

	resp, err := conn.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("tools/list request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("tools/list returned status %d", resp.StatusCode)
	}

	var result struct {
		Tools []struct {
			Name        string          `json:"name"`
			Description string          `json:"description"`
			InputSchema json.RawMessage `json:"inputSchema"`
		} `json:"tools"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decoding tools/list response: %w", err)
	}

	tools := make([]Tool, 0, len(result.Tools))
	for _, t := range result.Tools {
		tools = append(tools, Tool{
			// Prefix with server name to avoid collisions across multiple MCP servers.
			Name:         fmt.Sprintf("%s__%s", server.Name, t.Name),
			OriginalName: t.Name,
			Description:  t.Description,
			InputSchema:  t.InputSchema,
			ServerURL:    server.URL,
		})
	}
	return tools, nil
}

// Tools returns the full list of discovered MCP tools.
func (m *Manager) Tools() []Tool {
	return m.tools
}

// CallTool executes a tool on its MCP server and returns the text result.
func (m *Manager) CallTool(ctx context.Context, toolName string, input json.RawMessage) (string, error) {
	for _, t := range m.tools {
		if t.Name != toolName {
			continue
		}

		body, err := json.Marshal(map[string]any{
			"name":      t.OriginalName,
			"arguments": input,
		})
		if err != nil {
			return "", fmt.Errorf("marshalling tool call: %w", err)
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.ServerURL+"/tools/call",
			bytes.NewReader(body))
		if err != nil {
			return "", err
		}
		req.Header.Set("Content-Type", "application/json")

		conn := m.serverConns[t.ServerURL]
		conn.applyAuth(req)

		resp, err := conn.client.Do(req)
		if err != nil {
			return "", fmt.Errorf("tools/call request failed: %w", err)
		}
		defer func() { _ = resp.Body.Close() }()

		var result struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			return "", fmt.Errorf("decoding tools/call response: %w", err)
		}

		for _, c := range result.Content {
			if c.Type == contentTypeText {
				return c.Text, nil
			}
		}
		return "", nil
	}
	return "", fmt.Errorf("tool %q not found in any MCP server", toolName)
}

// Close is a no-op for HTTP-based MCP servers. Reserved for future SSE cleanup.
func (m *Manager) Close() {}
