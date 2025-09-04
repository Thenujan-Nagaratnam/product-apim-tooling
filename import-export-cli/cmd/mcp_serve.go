/*
*  Copyright (c) WSO2 Inc. (http://www.wso2.org) All Rights Reserved.
*
*  WSO2 Inc. licenses this file to you under the Apache License,
*  Version 2.0 (the "License"); you may not use this file except
*  in compliance with the License.
*  You may obtain a copy of the License at
*
*    http://www.apache.org/licenses/LICENSE-2.0
*
*  Unless required by applicable law or agreed to in writing,
*  software distributed under the License is distributed on an
*  "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY
*  KIND, either express or implied.  See the License for the
*  specific language governing permissions and limitations
*  under the License.
 */

package cmd

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/wso2/product-apim-tooling/import-export-cli/utils"
)

const mcpServeCmdLiteral = "serve"
const mcpServeCmdShortDesc = "Start MCP server over stdio"
const mcpServeCmdLongDesc = "Start a long-running Model Context Protocol (MCP) server over stdio for AI agents"

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
}

func runMCPServer(stdin io.Reader, stdout io.Writer, stderr io.Writer) {
	scanner := bufio.NewScanner(stdin)
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
		return jsonRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{"capabilities": map[string]any{}}}
	case "ping":
		return jsonRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]string{"status": "ok"}}
	case "tools/list":
		return jsonRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: listCommandsAsTools()}
	case "tools/call":
		return handleToolsCall(req)
	default:
		return jsonRPCResponse{JSONRPC: "2.0", ID: req.ID, Error: &jsonRPCError{Code: -32601, Message: "Method not found", Data: req.Method}}
	}
}

type toolsCallParams struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

func listCommandsAsTools() []map[string]any {
	var tools []map[string]any
	var walk func(prefix []string, c *cobra.Command)
	walk = func(prefix []string, c *cobra.Command) {
		for _, child := range c.Commands() {
			if child.Hidden {
				continue
			}
			// skip the MCP group itself
			if len(prefix) == 0 && child == MCPCmd {
				continue
			}
			path := append(prefix, child.Name())
			if child.Run != nil || child.RunE != nil {
				tools = append(tools, map[string]any{
					"name":        strings.Join(path, " "),
					"description": child.Short,
					"inputSchema": map[string]any{
						"type":       "object",
						"properties": map[string]any{"argv": map[string]any{"type": "array", "items": map[string]string{"type": "string"}}},
						"required":   []string{"argv"},
					},
				})
			}
			// recurse
			walk(path, child)
		}
	}
	walk([]string{}, RootCmd)
	return tools
}

func handleToolsCall(req jsonRPCRequest) jsonRPCResponse {
	var p toolsCallParams
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return jsonRPCResponse{JSONRPC: "2.0", ID: req.ID, Error: &jsonRPCError{Code: -32602, Message: "Invalid params", Data: err.Error()}}
		}
	}
	argv, err := extractArgv(p)
	if err != nil {
		return jsonRPCResponse{JSONRPC: "2.0", ID: req.ID, Error: &jsonRPCError{Code: -32602, Message: "Invalid params", Data: err.Error()}}
	}
	if len(argv) == 0 {
		return jsonRPCResponse{JSONRPC: "2.0", ID: req.ID, Error: &jsonRPCError{Code: -32602, Message: "argv is required"}}
	}
	// prevent recursive mcp calls
	if argv[0] == "mcp" {
		return jsonRPCResponse{JSONRPC: "2.0", ID: req.ID, Error: &jsonRPCError{Code: -32602, Message: "calling mcp via MCP is not allowed"}}
	}
	res := execApictl(argv)
	return jsonRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: res}
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
	// simple splitter on spaces; does not handle quotes/escapes
	return strings.Fields(s)
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
	cmd := exec.Command(exe, argv...)
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err = cmd.Run()
	exitCode := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && ee.ProcessState != nil {
			exitCode = ee.ProcessState.ExitCode()
		} else {
			exitCode = 1
		}
	}
	return apictlExecResult{Stdout: outBuf.String(), Stderr: errBuf.String(), ExitCode: exitCode}
}

func callGetEnvs(id interface{}) jsonRPCResponse {
	config := utils.GetMainConfigFromFile(utils.MainConfigFilePath)
	envs := config.Environments
	// minimize exposure to essential fields; include map key as name
	list := make([]map[string]string, 0, len(envs))
	for name, e := range envs {
		list = append(list, map[string]string{
			"name":                 name,
			"apiManagerEndpoint":   e.ApiManagerEndpoint,
			"registrationEndpoint": e.RegistrationEndpoint,
			"tokenEndpoint":        e.TokenEndpoint,
			"publisherEndpoint":    e.PublisherEndpoint,
			"devPortalEndpoint":    e.DevPortalEndpoint,
			"adminEndpoint":        e.AdminEndpoint,
			"miManagementEndpoint": e.MiManagementEndpoint,
		})
	}
	return jsonRPCResponse{JSONRPC: "2.0", ID: id, Result: list}
}
