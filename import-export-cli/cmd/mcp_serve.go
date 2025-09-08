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
	"github.com/spf13/pflag"
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

// Common structured tools we support with argument mapping
var structuredTools = []map[string]any{
	{
		"name":        "export_api",
		"description": "Export an API from an environment",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":                map[string]any{"type": "string"},
				"version":             map[string]any{"type": "string"},
				"environment":         map[string]any{"type": "string"},
				"provider":            map[string]any{"type": "string"},
				"rev":                 map[string]any{"type": "string"},
				"latest":              map[string]any{"type": "boolean"},
				"preserveStatus":      map[string]any{"type": "boolean"},
				"preserveCredentials": map[string]any{"type": "boolean"},
				"format":              map[string]any{"type": "string"},
			},
			"required": []string{"name", "version", "environment"},
		},
	},
	{
		"name":        "import_api",
		"description": "Import an API to an environment",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"file":             map[string]any{"type": "string"},
				"environment":      map[string]any{"type": "string"},
				"update":           map[string]any{"type": "boolean"},
				"params":           map[string]any{"type": "string"},
				"preserveProvider": map[string]any{"type": "boolean"},
				"rotateRevision":   map[string]any{"type": "boolean"},
				"skipDeployments":  map[string]any{"type": "boolean"},
				"skipCleanup":      map[string]any{"type": "boolean"},
				"dryRun":           map[string]any{"type": "boolean"},
				"format":           map[string]any{"type": "string"},
			},
			"required": []string{"file", "environment"},
		},
	},
	{
		"name":        "export_api_product",
		"description": "Export an API Product from an environment",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":           map[string]any{"type": "string"},
				"version":        map[string]any{"type": "string"},
				"environment":    map[string]any{"type": "string"},
				"provider":       map[string]any{"type": "string"},
				"rev":            map[string]any{"type": "string"},
				"latest":         map[string]any{"type": "boolean"},
				"preserveStatus": map[string]any{"type": "boolean"},
				"format":         map[string]any{"type": "string"},
			},
			"required": []string{"name", "version", "environment"},
		},
	},
	{
		"name":        "import_api_product",
		"description": "Import an API Product to an environment",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"file":             map[string]any{"type": "string"},
				"environment":      map[string]any{"type": "string"},
				"updateApiProduct": map[string]any{"type": "boolean"},
				"updateApis":       map[string]any{"type": "boolean"},
				"preserveProvider": map[string]any{"type": "boolean"},
				"rotateRevision":   map[string]any{"type": "boolean"},
				"skipDeployments":  map[string]any{"type": "boolean"},
				"skipCleanup":      map[string]any{"type": "boolean"},
				"params":           map[string]any{"type": "string"},
			},
			"required": []string{"file", "environment"},
		},
	},
	{
		"name":        "export_app",
		"description": "Export an Application from an environment",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":        map[string]any{"type": "string"},
				"owner":       map[string]any{"type": "string"},
				"environment": map[string]any{"type": "string"},
				"withKeys":    map[string]any{"type": "boolean"},
				"format":      map[string]any{"type": "string"},
			},
			"required": []string{"name", "owner", "environment"},
		},
	},
	{
		"name":        "import_app",
		"description": "Import an Application to an environment",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"file":              map[string]any{"type": "string"},
				"environment":       map[string]any{"type": "string"},
				"owner":             map[string]any{"type": "string"},
				"preserveOwner":     map[string]any{"type": "boolean"},
				"skipSubscriptions": map[string]any{"type": "boolean"},
				"skipKeys":          map[string]any{"type": "boolean"},
				"update":            map[string]any{"type": "boolean"},
				"skipCleanup":       map[string]any{"type": "boolean"},
			},
			"required": []string{"file", "environment"},
		},
	},
	// MCP Server
	{
		"name":        "export_mcp_server",
		"description": "Export an MCP Server from an environment",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":                map[string]any{"type": "string"},
				"version":             map[string]any{"type": "string"},
				"environment":         map[string]any{"type": "string"},
				"provider":            map[string]any{"type": "string"},
				"rev":                 map[string]any{"type": "string"},
				"latest":              map[string]any{"type": "boolean"},
				"preserveStatus":      map[string]any{"type": "boolean"},
				"preserveCredentials": map[string]any{"type": "boolean"},
				"format":              map[string]any{"type": "string"},
			},
			"required": []string{"name", "version", "environment"},
		},
	},
	{
		"name":        "import_mcp_server",
		"description": "Import an MCP Server to an environment",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"file":             map[string]any{"type": "string"},
				"environment":      map[string]any{"type": "string"},
				"update":           map[string]any{"type": "boolean"},
				"params":           map[string]any{"type": "string"},
				"preserveProvider": map[string]any{"type": "boolean"},
				"rotateRevision":   map[string]any{"type": "boolean"},
				"skipDeployments":  map[string]any{"type": "boolean"},
				"skipCleanup":      map[string]any{"type": "boolean"},
				"dryRun":           map[string]any{"type": "boolean"},
				"format":           map[string]any{"type": "string"},
			},
			"required": []string{"file", "environment"},
		},
	},
	// Policy export/import
	{
		"name":        "export_api_policy",
		"description": "Export an API Policy from an environment",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":        map[string]any{"type": "string"},
				"version":     map[string]any{"type": "string"},
				"environment": map[string]any{"type": "string"},
				"format":      map[string]any{"type": "string"},
			},
			"required": []string{"name", "version", "environment"},
		},
	},
	{
		"name":        "import_api_policy",
		"description": "Import an API Policy to an environment",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"file":        map[string]any{"type": "string"},
				"environment": map[string]any{"type": "string"},
			},
			"required": []string{"file", "environment"},
		},
	},
	// Logging
	{
		"name":        "set_api_logging",
		"description": "Set log level for an API",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"environment":  map[string]any{"type": "string"},
				"apiId":        map[string]any{"type": "string"},
				"tenantDomain": map[string]any{"type": "string"},
				"logLevel":     map[string]any{"type": "string"},
			},
			"required": []string{"environment", "apiId", "logLevel"},
		},
	},
	{
		"name":        "get_api_logging",
		"description": "Get API logging level(s)",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"environment":  map[string]any{"type": "string"},
				"apiId":        map[string]any{"type": "string"},
				"tenantDomain": map[string]any{"type": "string"},
				"format":       map[string]any{"type": "string"},
			},
			"required": []string{"environment"},
		},
	},
	{
		"name":        "set_correlation_logging",
		"description": "Set correlation logging component",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"environment":   map[string]any{"type": "string"},
				"componentName": map[string]any{"type": "string"},
				"enable":        map[string]any{"type": "boolean"},
				"deniedThreads": map[string]any{"type": "string"},
			},
			"required": []string{"environment", "componentName", "enable"},
		},
	},
	{
		"name":        "get_correlation_logging",
		"description": "Get correlation logging configuration",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"environment": map[string]any{"type": "string"},
				"format":      map[string]any{"type": "string"},
			},
			"required": []string{"environment"},
		},
	},
	// Add/Remove Env
	{
		"name":        "add_env",
		"description": "Add an environment to the config",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"environment":     map[string]any{"type": "string"},
				"apim":            map[string]any{"type": "string"},
				"registration":    map[string]any{"type": "string"},
				"publisher":       map[string]any{"type": "string"},
				"devportal":       map[string]any{"type": "string"},
				"token":           map[string]any{"type": "string"},
				"admin":           map[string]any{"type": "string"},
				"mi":              map[string]any{"type": "string"},
				"aiService":       map[string]any{"type": "string"},
				"aiTokenEndpoint": map[string]any{"type": "string"},
				"aiKey":           map[string]any{"type": "string"},
			},
			"required": []string{"environment"},
		},
	},
	{
		"name":        "remove_env",
		"description": "Remove an environment from the config",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"environment": map[string]any{"type": "string"},
			},
			"required": []string{"environment"},
		},
	},
	// Change Status
	{
		"name":        "change_status_api",
		"description": "Change API lifecycle status",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action":      map[string]any{"type": "string"},
				"name":        map[string]any{"type": "string"},
				"version":     map[string]any{"type": "string"},
				"provider":    map[string]any{"type": "string"},
				"environment": map[string]any{"type": "string"},
			},
			"required": []string{"action", "name", "version", "environment"},
		},
	},
	{
		"name":        "change_status_api_product",
		"description": "Change API Product lifecycle status",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action":      map[string]any{"type": "string"},
				"name":        map[string]any{"type": "string"},
				"version":     map[string]any{"type": "string"},
				"provider":    map[string]any{"type": "string"},
				"environment": map[string]any{"type": "string"},
			},
			"required": []string{"action", "name", "version", "environment"},
		},
	},
	{
		"name":        "change_status_mcp_server",
		"description": "Change MCP Server lifecycle status",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action":      map[string]any{"type": "string"},
				"name":        map[string]any{"type": "string"},
				"version":     map[string]any{"type": "string"},
				"provider":    map[string]any{"type": "string"},
				"environment": map[string]any{"type": "string"},
			},
			"required": []string{"action", "name", "version", "environment"},
		},
	},
	// Delete
	{
		"name":        "delete_api",
		"description": "Delete an API",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":        map[string]any{"type": "string"},
				"version":     map[string]any{"type": "string"},
				"provider":    map[string]any{"type": "string"},
				"environment": map[string]any{"type": "string"},
			},
			"required": []string{"name", "version", "environment"},
		},
	},
	{
		"name":        "delete_api_product",
		"description": "Delete an API Product",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":        map[string]any{"type": "string"},
				"version":     map[string]any{"type": "string"},
				"provider":    map[string]any{"type": "string"},
				"environment": map[string]any{"type": "string"},
			},
			"required": []string{"name", "version", "environment"},
		},
	},
	{
		"name":        "delete_mcp_server",
		"description": "Delete an MCP Server",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":        map[string]any{"type": "string"},
				"version":     map[string]any{"type": "string"},
				"provider":    map[string]any{"type": "string"},
				"environment": map[string]any{"type": "string"},
			},
			"required": []string{"name", "version", "environment"},
		},
	},
	{
		"name":        "delete_app",
		"description": "Delete an Application",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":        map[string]any{"type": "string"},
				"owner":       map[string]any{"type": "string"},
				"environment": map[string]any{"type": "string"},
			},
			"required": []string{"name", "environment"},
		},
	},
	// Get
	{
		"name":        "get_apis",
		"description": "List APIs",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"environment": map[string]any{"type": "string"},
				"query":       map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"limit":       map[string]any{"type": "string"},
				"format":      map[string]any{"type": "string"},
			},
			"required": []string{"environment"},
		},
	},
	{
		"name":        "get_api_products",
		"description": "List API Products",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"environment": map[string]any{"type": "string"},
				"query":       map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"limit":       map[string]any{"type": "string"},
				"format":      map[string]any{"type": "string"},
			},
			"required": []string{"environment"},
		},
	},
	{
		"name":        "get_apps",
		"description": "List Applications",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"environment": map[string]any{"type": "string"},
				"owner":       map[string]any{"type": "string"},
				"format":      map[string]any{"type": "string"},
			},
			"required": []string{"environment"},
		},
	},
	{
		"name":        "get_mcp_servers",
		"description": "List MCP Servers",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"environment": map[string]any{"type": "string"},
				"query":       map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"limit":       map[string]any{"type": "string"},
				"format":      map[string]any{"type": "string"},
			},
			"required": []string{"environment"},
		},
	},
	{
		"name":        "get_envs",
		"description": "List configured environments",
		"inputSchema": map[string]any{},
	},
	// Init project
	{
		"name":        "init",
		"description": "Initialize a new API project",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":         map[string]any{"type": "string"},
				"oas":          map[string]any{"type": "string"},
				"definition":   map[string]any{"type": "string"},
				"initialState": map[string]any{"type": "string"},
				"force":        map[string]any{"type": "boolean"},
			},
			"required": []string{"path"},
		},
	},
	// Undeploy
	{
		"name":        "undeploy_api",
		"description": "Undeploy an API revision from gateways",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":        map[string]any{"type": "string"},
				"version":     map[string]any{"type": "string"},
				"rev":         map[string]any{"type": "string"},
				"gatewayEnv":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"environment": map[string]any{"type": "string"},
			},
			"required": []string{"name", "version", "rev", "environment"},
		},
	},
	{
		"name":        "undeploy_api_product",
		"description": "Undeploy an API Product revision from gateways",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":        map[string]any{"type": "string"},
				"version":     map[string]any{"type": "string"},
				"rev":         map[string]any{"type": "string"},
				"gatewayEnv":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"environment": map[string]any{"type": "string"},
			},
			"required": []string{"name", "version", "rev", "environment"},
		},
	},
	{
		"name":        "undeploy_mcp_server",
		"description": "Undeploy an MCP Server revision from gateways",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":        map[string]any{"type": "string"},
				"version":     map[string]any{"type": "string"},
				"rev":         map[string]any{"type": "string"},
				"gatewayEnv":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"environment": map[string]any{"type": "string"},
			},
			"required": []string{"name", "version", "rev", "environment"},
		},
	},
}

func sanitizeToolName(s string) string {
	// replace spaces and hyphens with underscores
	t := strings.ReplaceAll(s, " ", "_")
	t = strings.ReplaceAll(t, "-", "_")
	return t
}

func listCommandsAsTools() []map[string]any {
	var tools []map[string]any
	tools = append(tools, structuredTools...)
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
			if child.Run != nil || child.RunE != nil {
				tools = append(tools, generateToolForCommand(strings.Join(path, " "), child))
			}
			walk(path, child)
		}
	}
	walk([]string{}, RootCmd)
	return tools
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
	})
	props["argv"] = map[string]any{"type": "array", "items": map[string]string{"type": "string"}}
	schema := map[string]any{"type": "object", "properties": props}
	return map[string]any{"name": sanitizeToolName(name), "description": c.Short, "inputSchema": schema}
}

func handleToolsCall(req jsonRPCRequest) jsonRPCResponse {
	var p toolsCallParams
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return jsonRPCResponse{JSONRPC: "2.0", ID: req.ID, Error: &jsonRPCError{Code: -32602, Message: "Invalid params", Data: err.Error()}}
		}
	}
	// Try curated structured mappings first
	if argv, ok, verr := buildArgvFromStructured(p.Name, p.Arguments); ok {
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
		return jsonRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: res}
	}
	// Try generic cobra mapping (normalize underscores to resolve)
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
		return jsonRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: res}
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
	return jsonRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: res}
}

func buildArgvFromCobraNormalized(name string, args map[string]any) ([]string, bool, error) {
	cmd := resolveCommandByPath(name)
	if cmd == nil {
		return nil, false, nil
	}
	pathTokens := tokenizeCommandPath(name)
	argv := append([]string{}, pathTokens...)
	used := 0
	cmd.Flags().VisitAll(func(f *pflag.Flag) {
		if f.Hidden {
			return
		}
		long := f.Name
		camel := kebabToCamel(long)
		if v, ok := args[long]; ok {
			appendFlagArg(&argv, f, v)
			used++
			return
		}
		if v, ok := args[camel]; ok {
			appendFlagArg(&argv, f, v)
			used++
			return
		}
		if f.Shorthand != "" {
			if v, ok := args[f.Shorthand]; ok {
				appendFlagArg(&argv, f, v)
				used++
				return
			}
		}
	})
	return argv, true, nil
}

func buildArgvFromStructured(name string, args map[string]any) ([]string, bool, error) {
	cmd := strings.ToLower(strings.TrimSpace(name))
	// allow both underscores and spaces in incoming name
	cmd = strings.ReplaceAll(cmd, "_", " ")
	switch cmd {
	case "export api":
		argv := []string{"export", "api"}
		nameV, okN := strArg(args, "name")
		verV, okV := strArg(args, "version")
		envV, okE := strArg(args, "environment")
		if !(okN && okV && okE) {
			return nil, true, fmt.Errorf("missing required fields: name, version, environment")
		}
		argv = append(argv, "-n", nameV, "-v", verV, "-e", envV)
		if v, ok := strArg(args, "provider"); ok {
			argv = append(argv, "-r", v)
		}
		if v, ok := strArg(args, "rev"); ok {
			argv = append(argv, "--rev", v)
		}
		if b, ok := boolArg(args, "latest"); ok && b {
			argv = append(argv, "--latest")
		}
		if b, ok := boolArg(args, "preserveStatus"); ok {
			if b {
				argv = append(argv, "--preserve-status")
			} else {
				argv = append(argv, "--preserve-status=false")
			}
		}
		if b, ok := boolArg(args, "preserveCredentials"); ok {
			if b {
				argv = append(argv, "--preserve-credentials")
			}
		}
		if v, ok := strArg(args, "format"); ok {
			argv = append(argv, "--format", v)
		}
		return argv, true, nil
	case "import api":
		argv := []string{"import", "api"}
		fileV, okF := strArg(args, "file")
		envV, okE := strArg(args, "environment")
		if !(okF && okE) {
			return nil, true, fmt.Errorf("missing required fields: file, environment")
		}
		argv = append(argv, "-f", fileV, "-e", envV)
		if b, ok := boolArg(args, "update"); ok && b {
			argv = append(argv, "--update")
		}
		if v, ok := strArg(args, "params"); ok {
			argv = append(argv, "--params", v)
		}
		if b, ok := boolArg(args, "preserveProvider"); ok {
			if b {
				argv = append(argv, "--preserve-provider")
			} else {
				argv = append(argv, "--preserve-provider=false")
			}
		}
		if b, ok := boolArg(args, "rotateRevision"); ok && b {
			argv = append(argv, "--rotate-revision")
		}
		if b, ok := boolArg(args, "skipDeployments"); ok && b {
			argv = append(argv, "--skip-deployments")
		}
		if b, ok := boolArg(args, "skipCleanup"); ok && b {
			argv = append(argv, "--skip-cleanup")
		}
		if b, ok := boolArg(args, "dryRun"); ok && b {
			argv = append(argv, "--dry-run")
		}
		if v, ok := strArg(args, "format"); ok {
			argv = append(argv, "--format", v)
		}
		return argv, true, nil
	case "export api-product":
		argv := []string{"export", "api-product"}
		nameV, okN := strArg(args, "name")
		verV, okV := strArg(args, "version")
		envV, okE := strArg(args, "environment")
		if !(okN && okV && okE) {
			return nil, true, fmt.Errorf("missing required fields: name, version, environment")
		}
		argv = append(argv, "-n", nameV, "-v", verV, "-e", envV)
		if v, ok := strArg(args, "provider"); ok {
			argv = append(argv, "-r", v)
		}
		if v, ok := strArg(args, "rev"); ok {
			argv = append(argv, "--rev", v)
		}
		if b, ok := boolArg(args, "latest"); ok && b {
			argv = append(argv, "--latest")
		}
		if b, ok := boolArg(args, "preserveStatus"); ok {
			if b {
				argv = append(argv, "--preserve-status")
			} else {
				argv = append(argv, "--preserve-status=false")
			}
		}
		if v, ok := strArg(args, "format"); ok {
			argv = append(argv, "--format", v)
		}
		return argv, true, nil
	case "import api-product":
		argv := []string{"import", "api-product"}
		fileV, okF := strArg(args, "file")
		envV, okE := strArg(args, "environment")
		if !(okF && okE) {
			return nil, true, fmt.Errorf("missing required fields: file, environment")
		}
		argv = append(argv, "-f", fileV, "-e", envV)
		if b, ok := boolArg(args, "updateApiProduct"); ok && b {
			argv = append(argv, "--update-api-product")
		}
		if b, ok := boolArg(args, "updateApis"); ok && b {
			argv = append(argv, "--update-apis")
		}
		if b, ok := boolArg(args, "preserveProvider"); ok {
			if b {
				argv = append(argv, "--preserve-provider")
			} else {
				argv = append(argv, "--preserve-provider=false")
			}
		}
		if b, ok := boolArg(args, "rotateRevision"); ok && b {
			argv = append(argv, "--rotate-revision")
		}
		if b, ok := boolArg(args, "skipDeployments"); ok && b {
			argv = append(argv, "--skip-deployments")
		}
		if b, ok := boolArg(args, "skipCleanup"); ok && b {
			argv = append(argv, "--skip-cleanup")
		}
		if v, ok := strArg(args, "params"); ok {
			argv = append(argv, "--params", v)
		}
		return argv, true, nil
	case "export app":
		argv := []string{"export", "app"}
		nameV, okN := strArg(args, "name")
		ownerV, okO := strArg(args, "owner")
		envV, okE := strArg(args, "environment")
		if !(okN && okO && okE) {
			return nil, true, fmt.Errorf("missing required fields: name, owner, environment")
		}
		argv = append(argv, "-n", nameV, "-o", ownerV, "-e", envV)
		if b, ok := boolArg(args, "withKeys"); ok && b {
			argv = append(argv, "--with-keys")
		}
		if v, ok := strArg(args, "format"); ok {
			argv = append(argv, "--format", v)
		}
		return argv, true, nil
	case "import app":
		argv := []string{"import", "app"}
		fileV, okF := strArg(args, "file")
		envV, okE := strArg(args, "environment")
		if !(okF && okE) {
			return nil, true, fmt.Errorf("missing required fields: file, environment")
		}
		argv = append(argv, "-f", fileV, "-e", envV)
		if v, ok := strArg(args, "owner"); ok {
			argv = append(argv, "-o", v)
		}
		if b, ok := boolArg(args, "preserveOwner"); ok && b {
			argv = append(argv, "--preserve-owner")
		}
		if b, ok := boolArg(args, "skipSubscriptions"); ok && b {
			argv = append(argv, "-s")
		}
		if b, ok := boolArg(args, "skipKeys"); ok && b {
			argv = append(argv, "--skip-keys")
		}
		if b, ok := boolArg(args, "update"); ok && b {
			argv = append(argv, "--update")
		}
		if b, ok := boolArg(args, "skipCleanup"); ok && b {
			argv = append(argv, "--skip-cleanup")
		}
		return argv, true, nil
	case "export mcp-server":
		argv := []string{"export", "mcp-server"}
		nameV, okN := strArg(args, "name")
		verV, okV := strArg(args, "version")
		envV, okE := strArg(args, "environment")
		if !(okN && okV && okE) {
			return nil, true, fmt.Errorf("missing required fields: name, version, environment")
		}
		argv = append(argv, "-n", nameV, "-v", verV, "-e", envV)
		if v, ok := strArg(args, "provider"); ok {
			argv = append(argv, "-r", v)
		}
		if v, ok := strArg(args, "rev"); ok {
			argv = append(argv, "--rev", v)
		}
		if b, ok := boolArg(args, "latest"); ok && b {
			argv = append(argv, "--latest")
		}
		if b, ok := boolArg(args, "preserveStatus"); ok {
			if b {
				argv = append(argv, "--preserve-status")
			} else {
				argv = append(argv, "--preserve-status=false")
			}
		}
		if b, ok := boolArg(args, "preserveCredentials"); ok {
			if b {
				argv = append(argv, "--preserve-credentials")
			}
		}
		if v, ok := strArg(args, "format"); ok {
			argv = append(argv, "--format", v)
		}
		return argv, true, nil
	case "import mcp-server":
		argv := []string{"import", "mcp-server"}
		fileV, okF := strArg(args, "file")
		envV, okE := strArg(args, "environment")
		if !(okF && okE) {
			return nil, true, fmt.Errorf("missing required fields: file, environment")
		}
		argv = append(argv, "-f", fileV, "-e", envV)
		if b, ok := boolArg(args, "update"); ok && b {
			argv = append(argv, "--update")
		}
		if v, ok := strArg(args, "params"); ok {
			argv = append(argv, "--params", v)
		}
		if b, ok := boolArg(args, "preserveProvider"); ok {
			if b {
				argv = append(argv, "--preserve-provider")
			} else {
				argv = append(argv, "--preserve-provider=false")
			}
		}
		if b, ok := boolArg(args, "rotateRevision"); ok && b {
			argv = append(argv, "--rotate-revision")
		}
		if b, ok := boolArg(args, "skipDeployments"); ok && b {
			argv = append(argv, "--skip-deployments")
		}
		if b, ok := boolArg(args, "skipCleanup"); ok && b {
			argv = append(argv, "--skip-cleanup")
		}
		if b, ok := boolArg(args, "dryRun"); ok && b {
			argv = append(argv, "--dry-run")
		}
		if v, ok := strArg(args, "format"); ok {
			argv = append(argv, "--format", v)
		}
		return argv, true, nil
	case "export api-policy":
		argv := []string{"export", "policy", "api"}
		nameV, okN := strArg(args, "name")
		verV, okV := strArg(args, "version")
		envV, okE := strArg(args, "environment")
		if !(okN && okV && okE) {
			return nil, true, fmt.Errorf("missing required fields: name, version, environment")
		}
		argv = append(argv, "-n", nameV, "-v", verV, "-e", envV)
		if v, ok := strArg(args, "format"); ok {
			argv = append(argv, "--format", v)
		}
		return argv, true, nil
	case "import api-policy":
		argv := []string{"import", "policy", "api"}
		fileV, okF := strArg(args, "file")
		envV, okE := strArg(args, "environment")
		if !(okF && okE) {
			return nil, true, fmt.Errorf("missing required fields: file, environment")
		}
		argv = append(argv, "-f", fileV, "-e", envV)
		return argv, true, nil
	case "set api-logging":
		argv := []string{"set", "api-logging"}
		env, okE := strArg(args, "environment")
		api, okI := strArg(args, "apiId")
		level, okL := strArg(args, "logLevel")
		if !(okE && okI && okL) {
			return nil, true, fmt.Errorf("missing required fields: environment, apiId, logLevel")
		}
		argv = append(argv, "-e", env, "--api-id", api, "--log-level", level)
		if v, ok := strArg(args, "tenantDomain"); ok {
			argv = append(argv, "--tenant-domain", v)
		}
		return argv, true, nil
	case "get api-logging":
		argv := []string{"get", "api-logging"}
		env, okE := strArg(args, "environment")
		if !okE {
			return nil, true, fmt.Errorf("missing required field: environment")
		}
		argv = append(argv, "-e", env)
		if v, ok := strArg(args, "apiId"); ok {
			argv = append(argv, "--api-id", v)
		}
		if v, ok := strArg(args, "tenantDomain"); ok {
			argv = append(argv, "--tenant-domain", v)
		}
		if v, ok := strArg(args, "format"); ok {
			argv = append(argv, "--format", v)
		}
		return argv, true, nil
	case "set correlation-logging":
		argv := []string{"set", "correlation-logging"}
		env, okE := strArg(args, "environment")
		comp, okC := strArg(args, "componentName")
		enableB, okEn := boolArg(args, "enable")
		if !(okE && okC && okEn) {
			return nil, true, fmt.Errorf("missing required fields: environment, componentName, enable")
		}
		argv = append(argv, "-e", env, "--component-name", comp, "--enable", fmt.Sprintf("%v", enableB))
		if v, ok := strArg(args, "deniedThreads"); ok {
			argv = append(argv, "--denied-threads", v)
		}
		return argv, true, nil
	case "get correlation-logging":
		argv := []string{"get", "correlation-logging"}
		env, okE := strArg(args, "environment")
		if !okE {
			return nil, true, fmt.Errorf("missing required field: environment")
		}
		argv = append(argv, "-e", env)
		if v, ok := strArg(args, "format"); ok {
			argv = append(argv, "--format", v)
		}
		return argv, true, nil
	case "add env":
		argv := []string{"add", "env"}
		env, ok := strArg(args, "environment")
		if !ok {
			return nil, true, fmt.Errorf("missing required field: environment")
		}
		argv = append(argv, env)
		if v, ok := strArg(args, "apim"); ok {
			argv = append(argv, "--apim", v)
		}
		if v, ok := strArg(args, "registration"); ok {
			argv = append(argv, "--registration", v)
		}
		if v, ok := strArg(args, "publisher"); ok {
			argv = append(argv, "--publisher", v)
		}
		if v, ok := strArg(args, "devportal"); ok {
			argv = append(argv, "--devportal", v)
		}
		if v, ok := strArg(args, "token"); ok {
			argv = append(argv, "--token", v)
		}
		if v, ok := strArg(args, "admin"); ok {
			argv = append(argv, "--admin", v)
		}
		if v, ok := strArg(args, "mi"); ok {
			argv = append(argv, "--mi", v)
		}
		if v, ok := strArg(args, "aiService"); ok {
			argv = append(argv, "--ai-service", v)
		}
		if v, ok := strArg(args, "aiTokenEndpoint"); ok {
			argv = append(argv, "--ai-token-endpoint", v)
		}
		if v, ok := strArg(args, "aiKey"); ok {
			argv = append(argv, "--ai-key", v)
		}
		return argv, true, nil
	case "remove env":
		argv := []string{"remove", "env"}
		env, ok := strArg(args, "environment")
		if !ok {
			return nil, true, fmt.Errorf("missing required field: environment")
		}
		argv = append(argv, env)
		return argv, true, nil
	case "change-status api":
		argv := []string{"change-status", "api"}
		action, okA := strArg(args, "action")
		nameV, okN := strArg(args, "name")
		verV, okV := strArg(args, "version")
		envV, okE := strArg(args, "environment")
		if !(okA && okN && okV && okE) {
			return nil, true, fmt.Errorf("missing required fields: action, name, version, environment")
		}
		argv = append(argv, "-a", action, "-n", nameV, "-v", verV, "-e", envV)
		if v, ok := strArg(args, "provider"); ok {
			argv = append(argv, "-r", v)
		}
		return argv, true, nil
	case "change-status api-product":
		argv := []string{"change-status", "api-product"}
		action, okA := strArg(args, "action")
		nameV, okN := strArg(args, "name")
		verV, okV := strArg(args, "version")
		envV, okE := strArg(args, "environment")
		if !(okA && okN && okV && okE) {
			return nil, true, fmt.Errorf("missing required fields: action, name, version, environment")
		}
		argv = append(argv, "-a", action, "-n", nameV, "-v", verV, "-e", envV)
		if v, ok := strArg(args, "provider"); ok {
			argv = append(argv, "-r", v)
		}
		return argv, true, nil
	case "change-status mcp-server":
		argv := []string{"change-status", "mcp-server"}
		action, okA := strArg(args, "action")
		nameV, okN := strArg(args, "name")
		verV, okV := strArg(args, "version")
		envV, okE := strArg(args, "environment")
		if !(okA && okN && okV && okE) {
			return nil, true, fmt.Errorf("missing required fields: action, name, version, environment")
		}
		argv = append(argv, "-a", action, "-n", nameV, "-v", verV, "-e", envV)
		if v, ok := strArg(args, "provider"); ok {
			argv = append(argv, "-r", v)
		}
		return argv, true, nil
	case "delete api":
		argv := []string{"delete", "api"}
		nameV, okN := strArg(args, "name")
		verV, okV := strArg(args, "version")
		envV, okE := strArg(args, "environment")
		if !(okN && okV && okE) {
			return nil, true, fmt.Errorf("missing required fields: name, version, environment")
		}
		argv = append(argv, "-n", nameV, "-v", verV, "-e", envV)
		if v, ok := strArg(args, "provider"); ok {
			argv = append(argv, "-r", v)
		}
		return argv, true, nil
	case "delete api-product":
		argv := []string{"delete", "api-product"}
		nameV, okN := strArg(args, "name")
		verV, okV := strArg(args, "version")
		envV, okE := strArg(args, "environment")
		if !(okN && okV && okE) {
			return nil, true, fmt.Errorf("missing required fields: name, version, environment")
		}
		argv = append(argv, "-n", nameV, "-v", verV, "-e", envV)
		if v, ok := strArg(args, "provider"); ok {
			argv = append(argv, "-r", v)
		}
		return argv, true, nil
	case "delete mcp-server":
		argv := []string{"delete", "mcp-server"}
		nameV, okN := strArg(args, "name")
		verV, okV := strArg(args, "version")
		envV, okE := strArg(args, "environment")
		if !(okN && okV && okE) {
			return nil, true, fmt.Errorf("missing required fields: name, version, environment")
		}
		argv = append(argv, "-n", nameV, "-v", verV, "-e", envV)
		if v, ok := strArg(args, "provider"); ok {
			argv = append(argv, "-r", v)
		}
		return argv, true, nil
	case "delete app":
		argv := []string{"delete", "app"}
		nameV, okN := strArg(args, "name")
		envV, okE := strArg(args, "environment")
		if !(okN && okE) {
			return nil, true, fmt.Errorf("missing required fields: name, environment")
		}
		argv = append(argv, "-n", nameV, "-e", envV)
		if v, ok := strArg(args, "owner"); ok {
			argv = append(argv, "-o", v)
		}
		return argv, true, nil
	case "get apis":
		argv := []string{"get", "apis"}
		envV, okE := strArg(args, "environment")
		if !okE {
			return nil, true, fmt.Errorf("missing required field: environment")
		}
		argv = append(argv, "-e", envV)
		if arr, ok := args["query"].([]any); ok {
			for _, it := range arr {
				if s, ok := it.(string); ok {
					argv = append(argv, "-q", s)
				}
			}
		}
		if v, ok := strArg(args, "limit"); ok {
			argv = append(argv, "-l", v)
		}
		if v, ok := strArg(args, "format"); ok {
			argv = append(argv, "--format", v)
		}
		return argv, true, nil
	case "get api-products":
		argv := []string{"get", "api-products"}
		envV, okE := strArg(args, "environment")
		if !okE {
			return nil, true, fmt.Errorf("missing required field: environment")
		}
		argv = append(argv, "-e", envV)
		if arr, ok := args["query"].([]any); ok {
			for _, it := range arr {
				if s, ok := it.(string); ok {
					argv = append(argv, "-q", s)
				}
			}
		}
		if v, ok := strArg(args, "limit"); ok {
			argv = append(argv, "-l", v)
		}
		if v, ok := strArg(args, "format"); ok {
			argv = append(argv, "--format", v)
		}
		return argv, true, nil
	case "get apps":
		argv := []string{"get", "apps"}
		envV, okE := strArg(args, "environment")
		if !okE {
			return nil, true, fmt.Errorf("missing required field: environment")
		}
		argv = append(argv, "-e", envV)
		if v, ok := strArg(args, "owner"); ok {
			argv = append(argv, "-o", v)
		}
		if v, ok := strArg(args, "format"); ok {
			argv = append(argv, "--format", v)
		}
		return argv, true, nil
	case "get mcp-servers":
		argv := []string{"get", "mcp-servers"}
		envV, okE := strArg(args, "environment")
		if !okE {
			return nil, true, fmt.Errorf("missing required field: environment")
		}
		argv = append(argv, "-e", envV)
		if arr, ok := args["query"].([]any); ok {
			for _, it := range arr {
				if s, ok := it.(string); ok {
					argv = append(argv, "-q", s)
				}
			}
		}
		if v, ok := strArg(args, "limit"); ok {
			argv = append(argv, "-l", v)
		}
		if v, ok := strArg(args, "format"); ok {
			argv = append(argv, "--format", v)
		}
		return argv, true, nil
	case "get envs":
		return []string{"get", "envs"}, true, nil
	case "init":
		argv := []string{"init"}
		pathV, okP := strArg(args, "path")
		if !okP {
			return nil, true, fmt.Errorf("missing required field: path")
		}
		argv = append(argv, pathV)
		if v, ok := strArg(args, "oas"); ok {
			argv = append(argv, "--oas", v)
		}
		if v, ok := strArg(args, "definition"); ok {
			argv = append(argv, "-d", v)
		}
		if v, ok := strArg(args, "initialState"); ok {
			argv = append(argv, "--initial-state", v)
		}
		if b, ok := boolArg(args, "force"); ok && b {
			argv = append(argv, "-f")
		}
		return argv, true, nil
	case "undeploy api":
		argv := []string{"undeploy", "api"}
		nameV, okN := strArg(args, "name")
		verV, okV := strArg(args, "version")
		revV, okR := strArg(args, "rev")
		envV, okE := strArg(args, "environment")
		if !(okN && okV && okR && okE) {
			return nil, true, fmt.Errorf("missing required fields: name, version, rev, environment")
		}
		argv = append(argv, "-n", nameV, "-v", verV, "--rev", revV, "-e", envV)
		if arr, ok := args["gatewayEnv"].([]any); ok {
			for _, it := range arr {
				if s, ok := it.(string); ok {
					argv = append(argv, "-g", s)
				}
			}
		}
		if v, ok := strArg(args, "provider"); ok {
			argv = append(argv, "-r", v)
		}
		return argv, true, nil
	case "undeploy api-product":
		argv := []string{"undeploy", "api-product"}
		nameV, okN := strArg(args, "name")
		verV, okV := strArg(args, "version")
		revV, okR := strArg(args, "rev")
		envV, okE := strArg(args, "environment")
		if !(okN && okV && okR && okE) {
			return nil, true, fmt.Errorf("missing required fields: name, version, rev, environment")
		}
		argv = append(argv, "-n", nameV, "-v", verV, "--rev", revV, "-e", envV)
		if arr, ok := args["gatewayEnv"].([]any); ok {
			for _, it := range arr {
				if s, ok := it.(string); ok {
					argv = append(argv, "-g", s)
				}
			}
		}
		return argv, true, nil
	case "undeploy mcp-server":
		argv := []string{"undeploy", "mcp-server"}
		nameV, okN := strArg(args, "name")
		verV, okV := strArg(args, "version")
		revV, okR := strArg(args, "rev")
		envV, okE := strArg(args, "environment")
		if !(okN && okV && okR && okE) {
			return nil, true, fmt.Errorf("missing required fields: name, version, rev, environment")
		}
		argv = append(argv, "-n", nameV, "-v", verV, "--rev", revV, "-e", envV)
		if arr, ok := args["gatewayEnv"].([]any); ok {
			for _, it := range arr {
				if s, ok := it.(string); ok {
					argv = append(argv, "-g", s)
				}
			}
		}
		return argv, true, nil
	default:
		return nil, false, nil
	}
}

func strArg(m map[string]any, k string) (string, bool) {
	if m == nil {
		return "", false
	}
	v, ok := m[k]
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	if !ok || strings.TrimSpace(s) == "" {
		return "", false
	}
	return s, true
}

func boolArg(m map[string]any, k string) (bool, bool) {
	if m == nil {
		return false, false
	}
	v, ok := m[k]
	if !ok {
		return false, false
	}
	b, ok := v.(bool)
	return b, ok
}

func tokenizeCommandPath(name string) []string {
	// Accept names with underscores or spaces; return argv path tokens matching cobra names
	s := strings.TrimSpace(name)
	if s == "" {
		return nil
	}
	// Split on underscores as primary delimiter
	parts := strings.Split(s, "_")
	if len(parts) == 1 {
		// fallback to spaces
		parts = strings.Fields(strings.ReplaceAll(s, "_", " "))
	}
	// For display argv, join multi-token segments with hyphens where cobra uses them will be handled by resolveCommandByPath; here we just return tokens as-is transformed with hyphens where appropriate is unknown, so leave as lower-cased
	for i := range parts {
		parts[i] = strings.ToLower(parts[i])
	}
	return parts
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
		if f.Shorthand != "" {
			if b {
				*argv = append(*argv, "-"+f.Shorthand)
			} else {
				*argv = append(*argv, "--"+f.Name+"=false")
			}
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
				s := fmt.Sprintf("%v", it)
				if f.Shorthand != "" {
					*argv = append(*argv, "-"+f.Shorthand, s)
				} else {
					*argv = append(*argv, "--"+f.Name, s)
				}
			}
			return
		}
		fallthrough
	default:
		s := fmt.Sprintf("%v", v)
		if f.Shorthand != "" {
			*argv = append(*argv, "-"+f.Shorthand, s)
			return
		}
		*argv = append(*argv, "--"+f.Name, s)
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
