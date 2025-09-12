/*
*  Copyright (c) 2025 WSO2 LLC. (http://www.wso2.org) All Rights Reserved.
*
*  WSO2 LLC. licenses this file to you under the Apache License,
*  Version 2.0 (the "License"); you may not use this file except
*  in compliance with the License.
*  You may obtain a copy of the License at
*
*    http://www.apache.org/licenses/LICENSE-2.0
*
* Unless required by applicable law or agreed to in writing,
* software distributed under the License is distributed on an
* "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY
* KIND, either express or implied.  See the License for the
* specific language governing permissions and limitations
* under the License.
 */

package cmd

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

const mcpServeCmdLiteral = "serve"
const mcpServeCmdShortDesc = "Start MCP server over stdio"
const mcpServeCmdLongDesc = "Start a long-running Model Context Protocol (MCP) server over stdio for AI agents"

const maxRequestBytes = 4 * 1024 * 1024 // 4 MiB

const maxExecDuration = 60 * time.Second
const maxOutputBytes = 4 * 1024 * 1024 // cap stdout/stderr per call

const defaultRequestTimeout = 30 * time.Second

var toolInclude []string // optional root-level allowlist
var toolExclude []string // optional root-level denylist

type jsonRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      interface{}     `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type jsonRPCResponse struct {
	JSONRPC string        `json:"jsonrpc"`
	ID      interface{}   `json:"id,omitempty"`
	Result  interface{}   `json:"result,omitempty"`
	Error   *jsonRPCError `json:"error,omitempty"`
}

type jsonRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

type initializeParams struct {
	ProtocolVersion string                 `json:"protocolVersion"`
	Capabilities    map[string]interface{} `json:"capabilities"`
	ClientInfo      *clientInfo            `json:"clientInfo,omitempty"`
}

type clientInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type serverInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type initializeResult struct {
	ProtocolVersion string                 `json:"protocolVersion"`
	Capabilities    map[string]interface{} `json:"capabilities"`
	ServerInfo      serverInfo             `json:"serverInfo"`
	Instructions    string                 `json:"instructions,omitempty"`
}

// JSON-RPC 2.0 Error Codes (from specification)
const (
	JSONRPCParseError           = -32700
	JSONRPCInvalidRequest       = -32600
	JSONRPCMethodNotFound       = -32601
	JSONRPCInvalidParams        = -32602
	JSONRPCInternalError        = -32603
	JSONRPCServerError          = -32000
	JSONRPCRequestCancelled     = -32800
	JSONRPCServerNotInitialized = -32002
)

var isInitialized uint32 // 0 = false, 1 = true
var shutdownChan = make(chan bool, 1)
var pendingRequests = sync.Map{}
var activeProgressTokens = sync.Map{}
var usedRequestIDs = sync.Map{}

// Serve MCP over stdio
var mcpServeCmd = &cobra.Command{
	Use:   mcpServeCmdLiteral,
	Short: mcpServeCmdShortDesc,
	Long:  mcpServeCmdLongDesc,
	Run: func(cmd *cobra.Command, args []string) {
		runMCPServer(os.Stdin, os.Stdout, os.Stderr)
	},
}

func init() {
	MCPCmd.AddCommand(mcpServeCmd)
	// Flags to control exposed tools
	mcpServeCmd.Flags().StringSliceVar(&toolInclude, "tool-include", nil, "Comma-separated root commands to include (allowlist)")
	mcpServeCmd.Flags().StringSliceVar(&toolExclude, "tool-exclude", nil, "Comma-separated root commands to exclude (denylist)")
}

func runMCPServer(stdin io.Reader, stdout io.Writer, stderr io.Writer) {
	// Setup signal handling for graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// Channel for scanner to communicate completion
	scanDone := make(chan error, 1)

	scanner := bufio.NewScanner(stdin)
	// Cap the maximum line size to prevent unbounded memory usage
	scanner.Buffer(make([]byte, 64*1024), maxRequestBytes)

	// Run scanner in goroutine
	go func() {
		defer close(scanDone)
		for scanner.Scan() {
			line := scanner.Bytes()

			// Validate UTF-8 encoding as required by MCP spec
			if !utf8.Valid(line) {
				logToStderr(stderr, "Received non-UTF-8 encoded message")
				writeJSON(stdout, jsonRPCResponse{
					JSONRPC: "2.0",
					Error:   &jsonRPCError{Code: JSONRPCInvalidRequest, Message: "Invalid request", Data: "Message must be UTF-8 encoded"},
				})
				continue
			}

			// Check for embedded newlines (forbidden by MCP spec)
			if bytes.Contains(line, []byte("\n")) || bytes.Contains(line, []byte("\r")) {
				logToStderr(stderr, "Received message with embedded newlines")
				writeJSON(stdout, jsonRPCResponse{
					JSONRPC: "2.0",
					Error:   &jsonRPCError{Code: JSONRPCInvalidRequest, Message: "Invalid request", Data: "Messages must not contain embedded newlines"},
				})
				continue
			}

			// Handle single JSON-RPC request
			var req jsonRPCRequest
			if err := json.Unmarshal(line, &req); err != nil {
				logToStderr(stderr, fmt.Sprintf("Failed to parse JSON-RPC request: %v", err))
				writeJSON(stdout, jsonRPCResponse{JSONRPC: "2.0", Error: &jsonRPCError{Code: JSONRPCInvalidRequest, Message: "Invalid request", Data: err.Error()}})
				continue
			}
			handleSingleRequest(req, stdout, stderr)
		}
		if err := scanner.Err(); err != nil {
			scanDone <- err
		}
	}()

	// Wait for shutdown signal or scanner completion
	select {
	case sig := <-sigChan:
		logToStderr(stderr, fmt.Sprintf("Received signal %v, shutting down gracefully...", sig))
		handleShutdown(stdout, stderr)
	case err := <-scanDone:
		if err != nil {
			logToStderr(stderr, fmt.Sprintf("Scanner error: %v", err))
		}
		logToStderr(stderr, "Input stream closed, shutting down...")
	case <-shutdownChan:
		logToStderr(stderr, "Server shutdown requested...")
	}

	// Wait a bit for any pending operations to complete
	time.Sleep(100 * time.Millisecond)
}

func writeJSON(w io.Writer, v any) {
	data, err := json.Marshal(v)
	if err != nil {
		// last resort write an internal error
		_fallback := jsonRPCResponse{JSONRPC: "2.0", Error: &jsonRPCError{Code: JSONRPCInternalError, Message: "Internal error", Data: err.Error()}}
		b, _ := json.Marshal(_fallback)
		writeValidatedJSON(w, b)
		return
	}
	writeValidatedJSON(w, data)
}

// writeValidatedJSON ensures JSON messages conform to MCP stdio transport requirements
func writeValidatedJSON(w io.Writer, data []byte) {
	// Ensure the message is valid UTF-8
	if !utf8.Valid(data) {
		// This should never happen with json.Marshal, but let's be safe
		logToStderr(os.Stderr, "Attempted to write non-UTF-8 JSON")
		return
	}

	// Ensure no embedded newlines (json.Marshal shouldn't produce these, but validate anyway)
	if bytes.Contains(data, []byte("\n")) || bytes.Contains(data, []byte("\r")) {
		// Replace any embedded newlines with spaces (shouldn't happen with json.Marshal)
		data = bytes.ReplaceAll(data, []byte("\n"), []byte(" "))
		data = bytes.ReplaceAll(data, []byte("\r"), []byte(" "))
		logToStderr(os.Stderr, "Warning: Removed embedded newlines from JSON output")
	}

	// Write the message followed by a newline (as required by MCP stdio transport)
	fmt.Fprintln(w, string(data))
}

// logToStderr writes UTF-8 logging messages to stderr (allowed by MCP spec)
func logToStderr(stderr io.Writer, message string) {
	// Ensure the log message is valid UTF-8
	if !utf8.ValidString(message) {
		message = "Invalid UTF-8 log message"
	}
	fmt.Fprintf(stderr, "[MCP Server] %s\n", message)
}

func dispatchMCP(req jsonRPCRequest) jsonRPCResponse {
	switch req.Method {
	case "initialize":
		return handleInitialize(req)
	case "notifications/initialized":
		return handleInitialized()
	case "notifications/cancelled":
		return handleCancellation(req)
	case "ping":
		return jsonRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{}}
	case "tools/list":
		if atomic.LoadUint32(&isInitialized) == 0 {
			return jsonRPCResponse{JSONRPC: "2.0", ID: req.ID, Error: &jsonRPCError{
				Code:    JSONRPCServerNotInitialized,
				Message: "Server not initialized",
				Data:    "Call 'initialize' method first",
			}}
		}
		return jsonRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{"tools": listCommandsAsTools()}}
	case "tools/call":
		if atomic.LoadUint32(&isInitialized) == 0 {
			return jsonRPCResponse{JSONRPC: "2.0", ID: req.ID, Error: &jsonRPCError{
				Code:    JSONRPCServerNotInitialized,
				Message: "Server not initialized",
				Data:    "Call 'initialize' method first",
			}}
		}
		return handleToolsCall(req)
	default:
		return jsonRPCResponse{JSONRPC: "2.0", ID: req.ID, Error: &jsonRPCError{
			Code:    JSONRPCMethodNotFound,
			Message: "Method not found",
			Data: map[string]interface{}{
				"method":    req.Method,
				"available": []string{"initialize", "notifications/initialized", "notifications/cancelled", "ping", "tools/list", "tools/call"},
			},
		}}
	}
}

func handleInitialize(req jsonRPCRequest) jsonRPCResponse {
	var params initializeParams
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return jsonRPCResponse{JSONRPC: "2.0", ID: req.ID, Error: &jsonRPCError{Code: JSONRPCInvalidParams, Message: "Invalid params", Data: err.Error()}}
		}
	}

	// Supported protocol versions (newest first)
	supportedVersions := []string{"2025-03-26", "2024-11-05"}
	negotiatedVersion := negotiateProtocolVersion(params.ProtocolVersion, supportedVersions)

	if negotiatedVersion == "" {
		return jsonRPCResponse{JSONRPC: "2.0", ID: req.ID, Error: &jsonRPCError{
			Code:    JSONRPCInvalidParams,
			Message: "Unsupported protocol version",
			Data:    fmt.Sprintf("Client requested: %s, Server supports: %v", params.ProtocolVersion, supportedVersions),
		}}
	}

	// Build server capabilities according to MCP specification
	capabilities := map[string]interface{}{
		"tools": map[string]interface{}{
			"listChanged": true,
		},
	}

	result := initializeResult{
		ProtocolVersion: negotiatedVersion,
		Capabilities:    capabilities,
		ServerInfo: serverInfo{
			Name:    "apictl",
			Version: "1.0",
		},
		Instructions: "This MCP server exposes WSO2 API Manager CLI (apictl) commands as tools. Note: Login and logout commands are not available through this MCP interface. Ensure you're authenticated with your target environments using 'apictl login <env>' in your terminal before using the tools.",
	}

	return jsonRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: result}
}

func handleInitialized() jsonRPCResponse {
	atomic.StoreUint32(&isInitialized, 1)
	// Notifications don't require a response according to JSON-RPC spec
	return jsonRPCResponse{} // Empty response (won't be written)
}

func handleCancellation(req jsonRPCRequest) jsonRPCResponse {
	// Parse cancellation notification
	var params struct {
		ID interface{} `json:"id"`
	}

	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &params); err == nil {
			// Try to cancel the pending request
			if _, exists := pendingRequests.LoadAndDelete(params.ID); exists {
				logToStderr(os.Stderr, fmt.Sprintf("Cancelled request %v", params.ID))
			}
		}
	}

	// Notifications don't require a response
	return jsonRPCResponse{}
}

func negotiateProtocolVersion(clientVersion string, supportedVersions []string) string {
	// First check if we support the client's requested version
	for _, supported := range supportedVersions {
		if clientVersion == supported {
			return clientVersion
		}
	}

	// If not, return our latest supported version
	if len(supportedVersions) > 0 {
		return supportedVersions[0]
	}

	return ""
}

func handleSingleRequest(req jsonRPCRequest, stdout io.Writer, stderr io.Writer) {
	// Validate JSON-RPC format
	if req.JSONRPC != "2.0" {
		writeJSON(stdout, jsonRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &jsonRPCError{Code: JSONRPCInvalidRequest, Message: "Invalid request", Data: "JSON-RPC version must be '2.0'"},
		})
		return
	}

	if req.Method == "" {
		writeJSON(stdout, jsonRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &jsonRPCError{Code: JSONRPCInvalidRequest, Message: "Invalid request", Data: "Method is required"},
		})
		return
	}

	// Validate ID requirements for requests (not notifications)
	if req.ID != nil {
		// Check for ID reuse (forbidden by MCP spec)
		if _, exists := usedRequestIDs.LoadOrStore(req.ID, true); exists {
			writeJSON(stdout, jsonRPCResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Error:   &jsonRPCError{Code: JSONRPCInvalidRequest, Message: "Invalid request", Data: "Request ID has been previously used in this session"},
			})
			return
		}

		go handleRequestWithTimeout(req, stdout, stderr)
	} else {
		// Notifications don't need timeout handling
		resp := dispatchMCP(req)
		if resp.JSONRPC != "" { // Only write response if there is one
			writeJSON(stdout, resp)
		}
	}
}

func handleRequestWithTimeout(req jsonRPCRequest, stdout io.Writer, stderr io.Writer) {
	// Store request in pending map
	pendingRequests.Store(req.ID, req)
	defer pendingRequests.Delete(req.ID)

	// Create timeout context
	ctx, cancel := context.WithTimeout(context.Background(), defaultRequestTimeout)
	defer cancel()

	// Channel to receive the response
	respChan := make(chan jsonRPCResponse, 1)

	// Process request in goroutine
	go func() {
		resp := dispatchMCP(req)
		respChan <- resp
	}()

	// Wait for response or timeout
	select {
	case resp := <-respChan:
		writeJSON(stdout, resp)
	case <-ctx.Done():
		// Timeout occurred
		timeoutResp := jsonRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error: &jsonRPCError{
				Code:    JSONRPCServerError, // Server error
				Message: "Request timeout",
				Data:    fmt.Sprintf("Request timed out after %v", defaultRequestTimeout),
			},
		}
		writeJSON(stdout, timeoutResp)
		logToStderr(stderr, fmt.Sprintf("Request %v timed out after %v", req.ID, defaultRequestTimeout))
	}
}

func handleShutdown(stdout io.Writer, stderr io.Writer) {
	// Cancel any pending requests
	pendingRequests.Range(func(key, value interface{}) bool {
		cancelResp := jsonRPCResponse{
			JSONRPC: "2.0",
			ID:      key,
			Error: &jsonRPCError{
				Code:    JSONRPCRequestCancelled, // Request cancelled
				Message: "Request cancelled due to server shutdown",
			},
		}
		writeJSON(stdout, cancelResp)
		pendingRequests.Delete(key)
		return true
	})

	// Clean up any active progress tokens
	activeProgressTokens.Range(func(key, value interface{}) bool {
		if tracker, ok := value.(*progressTracker); ok {
			tracker.sendProgress(100, floatPtr(100), "Operation cancelled due to server shutdown")
		}
		activeProgressTokens.Delete(key)
		return true
	})

	// Clear used request IDs for next session
	usedRequestIDs.Range(func(key, value interface{}) bool {
		usedRequestIDs.Delete(key)
		return true
	})

	logToStderr(stderr, "Shutdown complete")
}

func sanitizeToolName(s string) string {
	// replace spaces and hyphens with underscores
	t := strings.ReplaceAll(s, " ", "_")
	t = strings.ReplaceAll(t, "-", "_")
	return t
}

func listCommandsAsTools() []map[string]any {
	var tools []map[string]any
	var walk func(prefix []string, c *cobra.Command)
	walk = func(prefix []string, c *cobra.Command) {
		for _, child := range c.Commands() {
			if child.Hidden {
				continue
			}
			if len(prefix) == 0 && child == MCPCmd {
				continue
			}
			// Skip deprecated commands
			if child.Deprecated != "" {
				continue
			}

			path := append(prefix, child.Name())

			// Only include leaf runnable commands
			if (child.Run != nil || child.RunE != nil) && len(child.Commands()) == 0 {
				if shouldExposeRoot(path[0]) && shouldExposeCommand(strings.Join(path, " ")) {
					tools = append(tools, generateToolForCommand(strings.Join(path, " "), child))
				}
			}

			// Keep walking to reach leaves
			walk(path, child)
		}
	}
	walk([]string{}, RootCmd)
	return tools
}

// shouldExposeRoot decides if a root command should be exposed as a tool
func shouldExposeRoot(root string) bool {
	r := strings.ToLower(strings.TrimSpace(root))
	// If an allowlist is provided, only those are exposed
	if len(toolInclude) > 0 {
		for _, inc := range toolInclude {
			if r == strings.ToLower(strings.TrimSpace(inc)) {
				return true
			}
		}
		return false
	}
	// Apply denylist if provided
	if len(toolExclude) > 0 {
		for _, exc := range toolExclude {
			if r == strings.ToLower(strings.TrimSpace(exc)) {
				return false
			}
		}
	}
	return true
}

// shouldExposeCommand decides if a specific command should be exposed as a tool
func shouldExposeCommand(commandPath string) bool {
	cmd := strings.ToLower(strings.TrimSpace(commandPath))

	// Exclude login and logout commands
	if strings.Contains(cmd, "login") || strings.Contains(cmd, "logout") || strings.Contains(cmd, "completion") {
		return false
	}

	return true
}

func getRequiredFlags(cmd *cobra.Command) (req []string, oneOf []string) {
	cmd.Flags().VisitAll(func(f *pflag.Flag) {
		if f.Annotations == nil {
			return
		}
		if v, ok := f.Annotations["cobra_annotation_bash_completion_one_required_flag"]; ok && len(v) > 0 && strings.EqualFold(v[0], "true") {
			req = append(req, kebabToCamel(f.Name))
		}
	})
	return
}

func generateToolForCommand(name string, c *cobra.Command) map[string]any {
	props := map[string]any{}

	// Add positional arguments based on command usage and examples
	addPositionalArguments(name, c, props)

	// Add flags
	c.Flags().VisitAll(func(f *pflag.Flag) {
		if f.Hidden {
			return
		}
		jsonName := kebabToCamel(f.Name)
		switch f.Value.Type() {
		case "bool":
			props[jsonName] = map[string]any{"type": "boolean"}
		case "int", "int32", "int64":
			props[jsonName] = map[string]any{"type": "integer"}
		case "stringSlice":
			props[jsonName] = map[string]any{"type": "array", "items": map[string]string{"type": "string"}}
		default:
			props[jsonName] = map[string]any{"type": "string"}
		}
		if strings.TrimSpace(f.Usage) != "" {
			if m, ok := props[jsonName].(map[string]any); ok {
				m["description"] = f.Usage
				props[jsonName] = m
			}
		}
	})

	req, oneOf := getRequiredFlags(c)

	// Add positional arguments to required fields
	positionalRequired := getPositionalRequired(name, c)
	req = append(req, positionalRequired...)

	schema := map[string]any{"type": "object", "properties": props}
	if len(req) > 0 {
		schema["required"] = req
	}
	if len(oneOf) > 0 {
		anyOf := make([]map[string]any, 0, len(oneOf))
		for _, f := range oneOf {
			anyOf = append(anyOf, map[string]any{"required": []string{f}})
		}
		schema["anyOf"] = anyOf
	}
	return map[string]any{"name": sanitizeToolName(name), "description": c.Short, "inputSchema": schema}
}

func handleToolsCall(req jsonRPCRequest) jsonRPCResponse {
	var p toolsCallParams
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return jsonRPCResponse{JSONRPC: "2.0", ID: req.ID, Error: &jsonRPCError{Code: JSONRPCInvalidParams, Message: "Invalid params", Data: err.Error()}}
		}
	}

	// Set up progress tracking if a progress token is provided
	var progressTracker *progressTracker
	if p.Meta != nil && p.Meta.ProgressToken != nil {
		progressTracker = newProgressTracker(p.Meta.ProgressToken, os.Stdout, os.Stderr)
		activeProgressTokens.Store(p.Meta.ProgressToken, progressTracker)
		defer progressTracker.cleanup()
	}

	// Resolve command and arguments to an argv array
	var argv []string
	var err error

	if resolvedArgv, ok, verr := buildArgvFromCobraNormalized(p.Name, p.Arguments); ok {
		if verr != nil {
			return jsonRPCResponse{JSONRPC: "2.0", ID: req.ID, Error: &jsonRPCError{Code: JSONRPCInvalidParams, Message: verr.Error()}}
		}
		argv = resolvedArgv
	} else {
		// Fallback to explicit argv extraction
		argv, err = extractArgv(p)
		if err != nil {
			return jsonRPCResponse{JSONRPC: "2.0", ID: req.ID, Error: &jsonRPCError{Code: JSONRPCInvalidParams, Message: "Invalid params", Data: err.Error()}}
		}
	}

	// Validate and execute the command
	if len(argv) == 0 {
		// This can happen if name is empty and no argv/args are provided.
		return jsonRPCResponse{JSONRPC: "2.0", ID: req.ID, Error: &jsonRPCError{Code: JSONRPCInvalidParams, Message: "No command or arguments provided"}}
	}
	if argv[0] == "mcp" {
		return jsonRPCResponse{JSONRPC: "2.0", ID: req.ID, Error: &jsonRPCError{Code: JSONRPCInvalidParams, Message: "calling mcp via MCP is not allowed"}}
	}

	res := execApictlWithProgress(argv, progressTracker)

	// Combine stdout and stderr for better error reporting
	var output string
	if res.Stdout != "" && res.Stderr != "" {
		output = res.Stdout + "\n" + res.Stderr
	} else if res.Stdout != "" {
		output = res.Stdout
	} else if res.Stderr != "" {
		output = res.Stderr
	} else {
		output = fmt.Sprintf("Exit status %d", res.ExitCode)
	}

	return jsonRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{
		"content": []map[string]any{{"type": "text", "text": output}},
		"isError": res.ExitCode != 0,
	}}
}

func buildArgvFromCobraNormalized(name string, args map[string]any) ([]string, bool, error) {
	cmd := resolveCommandByPath(name)
	if cmd == nil {
		return nil, false, nil
	}
	full := strings.Fields(cmd.CommandPath())
	argv := []string{}
	if len(full) > 0 && full[0] == RootCmd.Name() {
		argv = append(argv, full[1:]...) // drop root name
	} else {
		argv = append(argv, full...)
	}
	cmd.Flags().VisitAll(func(f *pflag.Flag) {
		if f.Hidden {
			return
		}
		long := f.Name
		camel := kebabToCamel(long)
		if v, ok := args[long]; ok {
			appendFlagArg(&argv, f, v)
			return
		}
		if v, ok := args[camel]; ok {
			appendFlagArg(&argv, f, v)
			return
		}
		if f.Shorthand != "" {
			if v, ok := args[f.Shorthand]; ok {
				appendFlagArg(&argv, f, v)
				return
			}
		}
	})
	// Handle positional arguments based on command type
	handlePositionalArguments(name, args, &argv)

	// append user-provided positionals/argv (fallback)
	if raw, ok := args["argv"]; ok {
		if arr, ok := raw.([]any); ok {
			for _, v := range arr {
				argv = append(argv, fmt.Sprintf("%v", v))
			}
		}
	} else if raw, ok := args["positionals"]; ok {
		if arr, ok := raw.([]any); ok {
			for _, v := range arr {
				argv = append(argv, fmt.Sprintf("%v", v))
			}
		}
	} else if s, ok := args["args"].(string); ok && strings.TrimSpace(s) != "" {
		argv = append(argv, strings.Fields(s)...)
	}
	return argv, true, nil
}

func resolveCommandByPath(name string) *cobra.Command {
	s := strings.TrimSpace(name)
	if s == "" {
		return nil
	}
	// allow underscores and spaces; build tokens split by underscores or spaces
	raw := strings.ReplaceAll(s, "_", " ")
	tokens := strings.Fields(raw)
	if len(tokens) == 0 {
		return nil
	}
	cur := RootCmd
	for i := 0; i < len(tokens); {
		matched := false
		for _, c := range cur.Commands() {
			// try to match one or more tokens joined by '-' to the child name
			for j := i; j < len(tokens); j++ {
				candidate := strings.ToLower(strings.Join(tokens[i:j+1], "-"))
				if candidate == c.Name() {
					cur = c
					i = j + 1
					matched = true
					break
				}
			}
			if matched {
				break
			}
		}
		if !matched {
			return nil
		}
	}
	return cur
}

func kebabToCamel(s string) string {
	parts := strings.Split(s, "-")
	if len(parts) == 0 {
		return s
	}
	res := parts[0]
	for i := 1; i < len(parts); i++ {
		p := parts[i]
		if p == "" {
			continue
		}
		res += strings.ToUpper(p[:1]) + p[1:]
	}
	return res
}

func appendFlagArg(argv *[]string, f *pflag.Flag, v any) {
	switch f.Value.Type() {
	case "bool":
		b, ok := toBool(v)
		if !ok {
			return
		}
		if b {
			*argv = append(*argv, "--"+f.Name)
		} else {
			*argv = append(*argv, "--"+f.Name+"=false")
		}
	case "stringSlice":
		if arr, ok := v.([]any); ok {
			for _, it := range arr {
				*argv = append(*argv, "--"+f.Name, fmt.Sprintf("%v", it))
			}
			return
		}
		fallthrough
	default:
		*argv = append(*argv, "--"+f.Name, fmt.Sprintf("%v", v))
	}
}

func toBool(v any) (bool, bool) {
	switch t := v.(type) {
	case bool:
		return t, true
	case string:
		lower := strings.ToLower(strings.TrimSpace(t))
		if lower == "true" {
			return true, true
		}
		if lower == "false" {
			return false, true
		}
		return false, false
	default:
		return false, false
	}
}

type toolsCallParams struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
	Meta      *requestMeta   `json:"_meta,omitempty"`
}

// requestMeta contains metadata for requests, including progress tokens
type requestMeta struct {
	ProgressToken interface{} `json:"progressToken,omitempty"`
}

type progressParams struct {
	ProgressToken interface{} `json:"progressToken"`
	Progress      float64     `json:"progress"`
	Total         *float64    `json:"total,omitempty"`
	Message       string      `json:"message,omitempty"`
}

// progressTracker helps track and send progress updates
type progressTracker struct {
	token     interface{}
	stdout    io.Writer
	stderr    io.Writer
	lastSent  time.Time
	rateLimit time.Duration // minimum time between progress updates
}

const defaultProgressRateLimit = 100 * time.Millisecond // max 10 updates per second

// newProgressTracker creates a new progress tracker for the given token
func newProgressTracker(token interface{}, stdout, stderr io.Writer) *progressTracker {
	return &progressTracker{
		token:     token,
		stdout:    stdout,
		stderr:    stderr,
		rateLimit: defaultProgressRateLimit,
	}
}

// sendProgress sends a progress notification if rate limiting allows
func (pt *progressTracker) sendProgress(progress float64, total *float64, message string) {
	now := time.Now()
	if now.Sub(pt.lastSent) < pt.rateLimit {
		return // rate limited
	}

	pt.lastSent = now

	// Progress notifications are JSON-RPC notifications (no ID, no response expected)
	notification := map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  "notifications/progress",
		"params": progressParams{
			ProgressToken: pt.token,
			Progress:      progress,
			Total:         total,
			Message:       message,
		},
	}

	writeJSON(pt.stdout, notification)
	logToStderr(pt.stderr, fmt.Sprintf("Progress update: %s - %.1f", message, progress))
}

// cleanup removes the progress token from active tracking
func (pt *progressTracker) cleanup() {
	activeProgressTokens.Delete(pt.token)
}

func extractArgv(p toolsCallParams) ([]string, error) {
	if p.Arguments == nil {
		return nil, fmt.Errorf("arguments missing")
	}
	// preferred: explicit argv array
	if raw, ok := p.Arguments["argv"]; ok {
		if arr, ok := raw.([]any); ok {
			out := make([]string, 0, len(arr))
			for _, v := range arr {
				s, ok := v.(string)
				if !ok {
					return nil, fmt.Errorf("argv items must be strings")
				}
				out = append(out, s)
			}
			return out, nil
		}
		return nil, fmt.Errorf("argv must be an array of strings")
	}

	// Build command from name and handle positional arguments
	var parts []string
	if strings.TrimSpace(p.Name) != "" {
		parts = strings.Fields(p.Name)
	}

	// Handle positional arguments based on command type (same logic as buildArgvFromCobraNormalized)
	handlePositionalArguments(p.Name, p.Arguments, &parts)

	// Handle flags (try to resolve command to get flag definitions)
	cmd := resolveCommandByPath(p.Name)
	if cmd != nil {
		cmd.Flags().VisitAll(func(f *pflag.Flag) {
			if f.Hidden {
				return
			}
			long := f.Name
			camel := kebabToCamel(long)
			if v, ok := p.Arguments[long]; ok {
				appendFlagArg(&parts, f, v)
				return
			}
			if v, ok := p.Arguments[camel]; ok {
				appendFlagArg(&parts, f, v)
				return
			}
			if f.Shorthand != "" {
				if v, ok := p.Arguments[f.Shorthand]; ok {
					appendFlagArg(&parts, f, v)
					return
				}
			}
		})
	}

	// fallback: name as command path; optional args string
	if raw, ok := p.Arguments["args"]; ok {
		if s, ok := raw.(string); ok && strings.TrimSpace(s) != "" {
			parts = append(parts, splitShellLike(s)...)
		}
	}
	return parts, nil
}

func splitShellLike(s string) []string {
	// Minimal shell-like splitting supporting double/single quotes and escapes
	var parts []string
	var cur strings.Builder
	inSingle := false
	inDouble := false
	escaped := false
	for _, r := range s {
		if escaped {
			cur.WriteRune(r)
			escaped = false
			continue
		}
		switch r {
		case '\\':
			escaped = true
		case '\'':
			if !inDouble {
				inSingle = !inSingle
				continue
			}
			cur.WriteRune(r)
		case '"':
			if !inSingle {
				inDouble = !inDouble
				continue
			}
			cur.WriteRune(r)
		case ' ', '\t', '\n':
			if inSingle || inDouble {
				cur.WriteRune(r)
				continue
			}
			if cur.Len() > 0 {
				parts = append(parts, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 {
		parts = append(parts, cur.String())
	}
	return parts
}

type apictlExecResult struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exitCode"`
}

func execApictlWithProgress(argv []string, tracker *progressTracker) apictlExecResult {
	exe, err := os.Executable()
	if err != nil {
		return apictlExecResult{Stdout: "", Stderr: err.Error(), ExitCode: 1}
	}
	// resolve symlink if any
	exe, _ = filepath.EvalSymlinks(exe)

	ctx, cancel := context.WithTimeout(context.Background(), maxExecDuration)
	defer cancel()

	cmd := exec.CommandContext(ctx, exe, argv...)

	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf

	// Send initial progress if tracker is available
	if tracker != nil {
		commandName := strings.Join(argv, " ")
		if isLongRunningCommand(argv) {
			tracker.sendProgress(0, nil, fmt.Sprintf("Starting %s...", commandName))
		}
	}

	runErr := cmd.Start()
	if runErr != nil {
		return apictlExecResult{Stdout: "", Stderr: runErr.Error(), ExitCode: 1}
	}

	// Monitor progress for long-running commands
	var progressDone chan bool
	if tracker != nil && isLongRunningCommand(argv) {
		progressDone = make(chan bool)
		go monitorProgress(tracker, argv, progressDone)
	}

	waitErr := cmd.Wait()

	// Stop progress monitoring
	if progressDone != nil {
		progressDone <- true
		close(progressDone)
	}

	exitCode := 0
	if waitErr != nil {
		if ctx.Err() == context.DeadlineExceeded {
			_ = cmd.Process.Kill()
			if tracker != nil {
				tracker.sendProgress(100, floatPtr(100), "Operation timed out")
			}
			return apictlExecResult{Stdout: safeTruncate(outBuf.String(), maxOutputBytes), Stderr: "execution timed out", ExitCode: 124}
		}
		if ee, ok := waitErr.(*exec.ExitError); ok && ee.ProcessState != nil {
			exitCode = ee.ProcessState.ExitCode()
		} else {
			exitCode = 1
		}
	}

	// Send completion progress
	if tracker != nil && isLongRunningCommand(argv) {
		if exitCode == 0 {
			tracker.sendProgress(100, floatPtr(100), "Operation completed successfully")
		} else {
			tracker.sendProgress(100, floatPtr(100), "Operation completed with errors")
		}
	}

	return apictlExecResult{
		Stdout:   safeTruncate(outBuf.String(), maxOutputBytes),
		Stderr:   safeTruncate(errBuf.String(), maxOutputBytes),
		ExitCode: exitCode,
	}
}

// isLongRunningCommand determines if a command is likely to be long-running and benefit from progress tracking
func isLongRunningCommand(argv []string) bool {
	if len(argv) == 0 {
		return false
	}

	longRunningCommands := map[string]bool{
		"import": true,
		"export": true,
		"vcs":    true,
		"ai":     true,
		"bundle": true,
		"get":    true, // some get operations can be slow
	}

	return longRunningCommands[argv[0]]
}

// monitorProgress provides periodic progress updates for long-running operations
func monitorProgress(tracker *progressTracker, argv []string, done chan bool) {
	commandName := strings.Join(argv, " ")
	ticker := time.NewTicker(2 * time.Second) // Update every 2 seconds
	defer ticker.Stop()

	progress := 10.0 // Start at 10% after initial message
	increment := 5.0 // Increase by 5% each update

	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			if progress < 90 { // Don't go above 90% until completion
				progress += increment
				// Slow down progress as we get closer to completion
				if progress > 50 {
					increment = 2.0
				}
				tracker.sendProgress(progress, nil, fmt.Sprintf("Processing %s...", commandName))
			}
		}
	}
}

// floatPtr returns a pointer to the given float64 value
func floatPtr(f float64) *float64 {
	return &f
}

func safeTruncate(s string, max int) string {
	if max <= 0 {
		return ""
	}
	if len(s) <= max {
		return s
	}
	return s[:max]
}

// addPositionalArguments adds positional argument fields to the tool schema
func addPositionalArguments(name string, c *cobra.Command, props map[string]any) {
	// Use Cobra's built-in argument validation to determine positional arguments
	if c.Args != nil {
		// Create a mock validation function to extract argument requirements
		argSpec := extractArgumentSpec(c)
		for i, arg := range argSpec {
			argName := fmt.Sprintf("arg%d", i+1)
			if arg.Name != "" {
				argName = arg.Name
			}
			props[argName] = map[string]any{
				"type":        "string",
				"description": arg.Description,
			}
		}
	}
}

// argumentSpec represents a positional argument specification
type argumentSpec struct {
	Name        string
	Description string
	Required    bool
}

// extractArgumentSpec extracts argument specifications from Cobra command
func extractArgumentSpec(c *cobra.Command) []argumentSpec {
	var specs []argumentSpec

	// Always check command path first for common patterns
	commandPath := c.CommandPath()
	if strings.Contains(commandPath, "init") {
		specs = append(specs, argumentSpec{
			Name:        "projectName",
			Description: "Name of the API project to initialize",
			Required:    true,
		})
	} else if strings.Contains(commandPath, "login") || strings.Contains(commandPath, "logout") {
		specs = append(specs, argumentSpec{
			Name:        "environment",
			Description: "Environment name",
			Required:    true,
		})
	} else if strings.Contains(commandPath, "add env") || strings.Contains(commandPath, "remove env") {
		specs = append(specs, argumentSpec{
			Name:        "environmentName",
			Description: "Name of the environment",
			Required:    true,
		})
	}

	// If no specs found, try to extract from usage string
	if len(specs) == 0 && c.Args != nil {
		usage := c.Use

		// Parse usage string to extract argument patterns
		if strings.Contains(usage, "[environment]") {
			specs = append(specs, argumentSpec{
				Name:        "environment",
				Description: "Environment name",
				Required:    true,
			})
		}
		if strings.Contains(usage, "[environment-name]") {
			specs = append(specs, argumentSpec{
				Name:        "environmentName",
				Description: "Name of the environment",
				Required:    true,
			})
		}
		if strings.Contains(usage, "[project-name]") || strings.Contains(usage, "<project-name>") {
			specs = append(specs, argumentSpec{
				Name:        "projectName",
				Description: "Name of the project",
				Required:    true,
			})
		}
		if strings.Contains(usage, "[name]") && !strings.Contains(usage, "environment") {
			specs = append(specs, argumentSpec{
				Name:        "name",
				Description: "Name parameter",
				Required:    true,
			})
		}
	}

	return specs
}

// getPositionalRequired returns the required positional argument field names
func getPositionalRequired(name string, c *cobra.Command) []string {
	var required []string

	// Use the same argument specification extraction
	argSpecs := extractArgumentSpec(c)
	for _, spec := range argSpecs {
		if spec.Required {
			required = append(required, spec.Name)
		}
	}

	return required
}

// handlePositionalArguments processes positional arguments and adds them to argv
func handlePositionalArguments(name string, args map[string]any, argv *[]string) {
	// Try to resolve the command to get its argument specifications
	cmd := resolveCommandByPath(name)
	if cmd == nil {
		return
	}

	// Get argument specifications for this command
	argSpecs := extractArgumentSpec(cmd)

	// Add positional arguments in order
	for _, spec := range argSpecs {
		if value, ok := args[spec.Name].(string); ok && value != "" {
			*argv = append(*argv, value)
		}
	}
}
