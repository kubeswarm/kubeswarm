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

// Package grpcprovider implements LLMProvider by forwarding calls to an external
// gRPC plugin service (RFC-0025). It is registered under the name "grpc" and is
// always compiled into the core binary. When SWARM_PLUGIN_LLM_ADDR is set the agent
// selects this provider instead of a built-in one.
package grpcprovider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/kubeswarm/kubeswarm/pkg/agent/config"
	agenterrors "github.com/kubeswarm/kubeswarm/pkg/agent/errors"
	"github.com/kubeswarm/kubeswarm/pkg/agent/mcp"
	"github.com/kubeswarm/kubeswarm/pkg/agent/providers"
	"github.com/kubeswarm/kubeswarm/pkg/agent/queue"
	pluginv1 "github.com/kubeswarm/kubeswarm/proto/pluginv1"
)

func init() {
	providers.Register("grpc", func() providers.LLMProvider { return &Provider{} })
}

// Provider implements providers.LLMProvider by forwarding RunTask calls to an
// external gRPC service at the address stored in cfg.ExternalProviderAddr.
type Provider struct{}

// RunTask opens a bidirectional gRPC stream to the plugin service and drives the
// full agentic tool-use loop: init -> [chunk/tool_call/tool_result]* -> result.
func (p *Provider) RunTask(
	ctx context.Context,
	cfg *config.Config,
	task queue.Task,
	tools []mcp.Tool,
	callTool func(context.Context, string, json.RawMessage) (string, error),
	chunkFn func(string),
) (string, queue.TokenUsage, error) {
	if cfg.ExternalProviderAddr == "" {
		return "", queue.TokenUsage{}, agenterrors.NewConfigError(
			agenterrors.ErrConfigMissing,
			"grpc provider: ExternalProviderAddr is not set",
			nil,
		)
	}

	conn, err := grpc.NewClient(cfg.ExternalProviderAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return "", queue.TokenUsage{}, agenterrors.NewLLMError(
			agenterrors.ErrLLMProviderError,
			fmt.Sprintf("grpc provider: dial %s: %v", cfg.ExternalProviderAddr, err),
			err,
		)
	}
	defer func() { _ = conn.Close() }()

	client := pluginv1.NewLLMProviderClient(conn)
	stream, err := client.RunTask(ctx)
	if err != nil {
		return "", queue.TokenUsage{}, agenterrors.NewLLMError(
			agenterrors.ErrLLMProviderError,
			fmt.Sprintf("grpc provider: open stream: %v", err),
			err,
		)
	}

	// Send the initial RunTaskRequest to open the loop.
	if err := stream.Send(&pluginv1.RunTaskMessage{
		Payload: &pluginv1.RunTaskMessage_Init{
			Init: buildRequest(cfg, task, tools),
		},
	}); err != nil {
		return "", queue.TokenUsage{}, agenterrors.NewLLMError(
			agenterrors.ErrLLMProviderError,
			fmt.Sprintf("grpc provider: send init: %v", err),
			err,
		)
	}

	var usage queue.TokenUsage

	for {
		msg, err := stream.Recv()
		if err == io.EOF {
			return "", usage, agenterrors.NewLLMError(
				agenterrors.ErrLLMProviderError,
				"grpc provider: stream closed before Result",
				err,
			)
		}
		if err != nil {
			code := agenterrors.ErrLLMProviderError
			if ctx.Err() != nil {
				code = agenterrors.ErrLLMTimeout
			}
			return "", usage, agenterrors.NewLLMError(
				code,
				fmt.Sprintf("grpc provider: recv: %v", err),
				err,
			)
		}

		switch p := msg.Payload.(type) {
		case *pluginv1.RunTaskMessage_Chunk:
			if chunkFn != nil {
				chunkFn(p.Chunk.Text)
			}

		case *pluginv1.RunTaskMessage_ToolCall:
			tc := p.ToolCall
			output, toolErr := callTool(ctx, tc.ToolName, json.RawMessage(tc.Input))
			errStr := ""
			if toolErr != nil {
				errStr = toolErr.Error()
				output = ""
			}
			if err := stream.Send(&pluginv1.RunTaskMessage{
				Payload: &pluginv1.RunTaskMessage_ToolResult{
					ToolResult: &pluginv1.ToolCallResult{
						CallId: tc.CallId,
						Output: output,
						Error:  errStr,
					},
				},
			}); err != nil {
				return "", usage, agenterrors.NewLLMError(
					agenterrors.ErrLLMProviderError,
					fmt.Sprintf("grpc provider: send tool result: %v", err),
					err,
				)
			}

		case *pluginv1.RunTaskMessage_Result:
			r := p.Result
			usage.InputTokens = r.InputTokens
			usage.OutputTokens = r.OutputTokens
			if r.Error != "" {
				return "", usage, agenterrors.NewLLMError(
					agenterrors.ErrLLMProviderError,
					r.Error,
					nil,
				)
			}
			return r.Output, usage, nil
		}
	}
}

func buildRequest(cfg *config.Config, task queue.Task, tools []mcp.Tool) *pluginv1.RunTaskRequest {
	protoTools := make([]*pluginv1.Tool, 0, len(tools))
	for _, t := range tools {
		protoTools = append(protoTools, &pluginv1.Tool{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: t.InputSchema,
		})
	}
	return &pluginv1.RunTaskRequest{
		TaskId:       task.ID,
		Prompt:       task.Prompt,
		SystemPrompt: cfg.SystemPrompt,
		Model:        cfg.Model,
		MaxTokens:    int32(cfg.MaxTokensPerCall),
		Tools:        protoTools,
	}
}

// Embed implements providers.LLMProvider.
// The gRPC plugin protocol does not define an Embed RPC yet (planned for a future RFC),
// so this always returns ErrEmbeddingNotSupported.
func (p *Provider) Embed(_ context.Context, _ string) ([]float32, error) {
	return nil, providers.ErrEmbeddingNotSupported
}
