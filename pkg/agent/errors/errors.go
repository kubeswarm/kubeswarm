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

package errors

import "fmt"

// ErrorCode identifies the category of an agent runtime error.
type ErrorCode string

const (
	ErrLLMTimeout         ErrorCode = "LLMTimeout"
	ErrLLMAuthFailed      ErrorCode = "LLMAuthFailed"
	ErrLLMRateLimited     ErrorCode = "LLMRateLimited"
	ErrLLMContextExceeded ErrorCode = "LLMContextExceeded"
	ErrLLMProviderError   ErrorCode = "LLMProviderError"
	ErrToolTimeout        ErrorCode = "ToolTimeout"
	ErrToolNotFound       ErrorCode = "ToolNotFound"
	ErrToolExecFailed     ErrorCode = "ToolExecutionFailed"
	ErrToolInvalidArgs    ErrorCode = "ToolInvalidArgs"
	ErrToolDenied         ErrorCode = "ToolDenied"
	ErrMemoryUnavailable  ErrorCode = "MemoryUnavailable"
	ErrMemoryQueryFailed  ErrorCode = "MemoryQueryFailed"
	ErrQueueFull          ErrorCode = "QueueFull"
	ErrQueueTimeout       ErrorCode = "QueueTimeout"
	ErrConfigInvalid      ErrorCode = "ConfigInvalid"
	ErrConfigMissing      ErrorCode = "ConfigMissing"
)

// defaultSuggestions maps each error code to a user-facing hint for resolution.
var defaultSuggestions = map[ErrorCode]string{
	ErrLLMTimeout:         "Increase spec.guardrails.limits.timeoutSeconds or check provider status",
	ErrLLMAuthFailed:      "Verify spec.apiKeyRef points to a valid Secret with the correct key",
	ErrLLMRateLimited:     "Reduce concurrentTasks or add a rate-limit delay",
	ErrLLMContextExceeded: "Reduce prompt size or increase tokensPerCall",
	ErrLLMProviderError:   "Check provider status page; the error may be transient",
	ErrToolTimeout:        "Increase tool timeout or check MCP server health",
	ErrToolNotFound:       "Check that the MCP server is running and the tool name matches",
	ErrToolExecFailed:     "Check MCP server logs for the root cause",
	ErrToolInvalidArgs:    "Verify the tool's input schema matches the arguments the model sent",
	ErrToolDenied:         "Check SwarmPolicy tool deny configuration",
	ErrMemoryUnavailable:  "Check vector store connectivity and credentials",
	ErrMemoryQueryFailed:  "Check vector store logs; the query may be malformed",
	ErrQueueFull:          "Increase queue capacity or reduce task submission rate",
	ErrQueueTimeout:       "Check Redis connectivity and queue health",
	ErrConfigInvalid:      "Check agent environment variables and SwarmAgent spec",
	ErrConfigMissing:      "Ensure required environment variables are set by the operator",
}

// AgentError is a structured error carrying a code, component, message,
// suggestion, and optional wrapped cause.
type AgentError struct {
	Code       ErrorCode
	Component  string // "llm", "tool", "memory", "queue", "config"
	Message    string
	Suggestion string
	Cause      error
}

// Error formats the error as "[Code] Message".
func (e *AgentError) Error() string {
	return fmt.Sprintf("[%s] %s", e.Code, e.Message)
}

// Unwrap returns the underlying cause for use with errors.Is / errors.As.
func (e *AgentError) Unwrap() error {
	return e.Cause
}

// NewAgentError creates an AgentError with the given component.
func NewAgentError(component string, code ErrorCode, msg string, cause error) *AgentError {
	return &AgentError{
		Code:       code,
		Component:  component,
		Message:    msg,
		Suggestion: defaultSuggestions[code],
		Cause:      cause,
	}
}

// NewLLMError creates an AgentError with Component="llm".
func NewLLMError(code ErrorCode, msg string, cause error) *AgentError {
	return NewAgentError("llm", code, msg, cause)
}

// NewToolError creates an AgentError with Component="tool".
func NewToolError(code ErrorCode, msg string, cause error) *AgentError {
	return NewAgentError("tool", code, msg, cause)
}

// NewMemoryError creates an AgentError with Component="memory".
func NewMemoryError(code ErrorCode, msg string, cause error) *AgentError {
	return NewAgentError("memory", code, msg, cause)
}

// NewConfigError creates an AgentError with Component="config".
func NewConfigError(code ErrorCode, msg string, cause error) *AgentError {
	return NewAgentError("config", code, msg, cause)
}

// NewQueueError creates a queue-component AgentError.
func NewQueueError(code ErrorCode, msg string, cause error) *AgentError {
	return NewAgentError("queue", code, msg, cause)
}
