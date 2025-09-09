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
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

const mcpServeCmdLiteral = "serve"
const mcpServeCmdShortDesc = "Start MCP server over stdio"
const mcpServeCmdLongDesc = "Start a long-running Model Context Protocol (MCP) server over stdio for AI agents"

// Security/resource limits
// Max size of a single JSON-RPC line read from stdin. Increase if your payloads are larger.
const maxRequestBytes = 4 * 1024 * 1024 // 4 MiB

// Subprocess execution constraints
const maxExecDuration = 60 * time.Second
const maxOutputBytes = 4 * 1024 * 1024 // cap stdout/stderr per call

// Tool exposure configuration
var toolMode string      // "all" | "minimal"
var toolInclude []string // optional root-level allowlist
var toolExclude []string // optional root-level denylist
var minimalExcluded = map[string]struct{}{
	"ai": {}, "bundle": {}, "gen": {}, "mcp": {}, "mi": {}, "mg": {},
	"secret": {}, "k8s": {}, "aws": {}, "vcs": {}, "completion": {},
}

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
	mcpServeCmd.Flags().StringVar(&toolMode, "tool-mode", "all", "Tool exposure mode: all|minimal")
	mcpServeCmd.Flags().StringSliceVar(&toolInclude, "tool-include", nil, "Comma-separated root commands to include (allowlist)")
	mcpServeCmd.Flags().StringSliceVar(&toolExclude, "tool-exclude", nil, "Comma-separated root commands to exclude (denylist)")
}

func runMCPServer(stdin io.Reader, stdout io.Writer, stderr io.Writer) {
	scanner := bufio.NewScanner(stdin)
	// Cap the maximum line size to prevent unbounded memory usage
	scanner.Buffer(make([]byte, 64*1024), maxRequestBytes)
	for scanner.Scan() {
		line := scanner.Bytes()
		var req jsonRPCRequest
		if err := json.Unmarshal(line, &req); err != nil {
			writeJSON(stdout, jsonRPCResponse{JSONRPC: "2.0", Error: &jsonRPCError{Code: -32600, Message: "Invalid request", Data: err.Error()}})
			continue
		}
		resp := dispatchMCP(req)
		writeJSON(stdout, resp)
	}
	if err := scanner.Err(); err != nil {
		fmt.Fprintln(stderr, "scanner error:", err)
	}
}

func writeJSON(w io.Writer, v any) {
	bytes, err := json.Marshal(v)
	if err != nil {
		// last resort write an internal error
		_fallback := jsonRPCResponse{JSONRPC: "2.0", Error: &jsonRPCError{Code: -32603, Message: "Internal error", Data: err.Error()}}
		b, _ := json.Marshal(_fallback)
		fmt.Fprintln(w, string(b))
		return
	}
	fmt.Fprintln(w, string(bytes))
}

func dispatchMCP(req jsonRPCRequest) jsonRPCResponse {
	switch req.Method {
	case "initialize":
		return jsonRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "apictl", "version": "dev"},
		}}
	case "ping":
		return jsonRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]string{"status": "ok"}}
	case "tools/list":
		return jsonRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{"tools": listCommandsAsTools()}}
	case "tools/call":
		return handleToolsCall(req)
	default:
		return jsonRPCResponse{JSONRPC: "2.0", ID: req.ID, Error: &jsonRPCError{Code: -32601, Message: "Method not found", Data: req.Method}}
	}
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

			path := append(prefix, child.Name())

			// Only include leaf runnable commands
			if (child.Run != nil || child.RunE != nil) && len(child.Commands()) == 0 {
				if shouldExposeRoot(path[0]) {
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
	// Minimal mode blocks a predefined set
	if strings.EqualFold(toolMode, "minimal") {
		if _, blocked := minimalExcluded[r]; blocked {
			return false
		}
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
			return jsonRPCResponse{JSONRPC: "2.0", ID: req.ID, Error: &jsonRPCError{Code: -32602, Message: "Invalid params", Data: err.Error()}}
		}
	}
	// Map arguments dynamically from Cobra
	if argv, ok, verr := buildArgvFromCobraNormalized(p.Name, p.Arguments); ok {
		if verr != nil {
			return jsonRPCResponse{JSONRPC: "2.0", ID: req.ID, Error: &jsonRPCError{Code: -32602, Message: verr.Error()}}
		}
		if len(argv) == 0 {
			return jsonRPCResponse{JSONRPC: "2.0", ID: req.ID, Error: &jsonRPCError{Code: -32602, Message: "no arguments resolved"}}
		}
		if argv[0] == "mcp" {
			return jsonRPCResponse{JSONRPC: "2.0", ID: req.ID, Error: &jsonRPCError{Code: -32602, Message: "calling mcp via MCP is not allowed"}}
		}
		res := execApictl(argv)
		return jsonRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{
			"content": []map[string]any{{"type": "text", "text": res.Stdout}},
			"isError": res.ExitCode != 0,
		}}
	}
	// Fallback to explicit argv
	argv, err := extractArgv(p)
	if err != nil {
		return jsonRPCResponse{JSONRPC: "2.0", ID: req.ID, Error: &jsonRPCError{Code: -32602, Message: "Invalid params", Data: err.Error()}}
	}
	if len(argv) == 0 {
		return jsonRPCResponse{JSONRPC: "2.0", ID: req.ID, Error: &jsonRPCError{Code: -32602, Message: "argv is required"}}
	}
	if argv[0] == "mcp" {
		return jsonRPCResponse{JSONRPC: "2.0", ID: req.ID, Error: &jsonRPCError{Code: -32602, Message: "calling mcp via MCP is not allowed"}}
	}
	res := execApictl(argv)
	return jsonRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{
		"content": []map[string]any{{"type": "text", "text": res.Stdout}},
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
	// append user-provided positionals/argv
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
	// fallback: name as command path; optional args string
	var parts []string
	if strings.TrimSpace(p.Name) != "" {
		parts = strings.Fields(p.Name)
	}
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

func execApictl(argv []string) apictlExecResult {
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

	runErr := cmd.Start()
	if runErr != nil {
		return apictlExecResult{Stdout: "", Stderr: runErr.Error(), ExitCode: 1}
	}
	waitErr := cmd.Wait()

	exitCode := 0
	if waitErr != nil {
		if ctx.Err() == context.DeadlineExceeded {
			_ = cmd.Process.Kill()
			return apictlExecResult{Stdout: safeTruncate(outBuf.String(), maxOutputBytes), Stderr: "execution timed out", ExitCode: 124}
		}
		if ee, ok := waitErr.(*exec.ExitError); ok && ee.ProcessState != nil {
			exitCode = ee.ProcessState.ExitCode()
		} else {
			exitCode = 1
		}
	}

	return apictlExecResult{
		Stdout:   safeTruncate(outBuf.String(), maxOutputBytes),
		Stderr:   safeTruncate(errBuf.String(), maxOutputBytes),
		ExitCode: exitCode,
	}
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
