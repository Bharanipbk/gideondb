// Package mcp implements a Model Context Protocol server that exposes a
// GideonDB deployment to AI agents: search, upsert, collection admin and a
// long-term memory API. It speaks JSON-RPC 2.0 over newline-delimited stdio
// and talks to GideonDB through the public REST client.
package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
)

// ProtocolVersion is the MCP revision this server implements.
const ProtocolVersion = "2025-06-18"

const maxMessageBytes = 16 << 20

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

const (
	codeParseError     = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
)

// Tool is one callable MCP tool.
type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
	Annotations map[string]any `json:"annotations,omitempty"`
	handler     func(ctx context.Context, arguments json.RawMessage) (any, error)
}

// Server dispatches MCP requests to tools.
type Server struct {
	name    string
	version string
	tools   []Tool
	byName  map[string]*Tool
}

// NewServer returns a server advertising the given tools.
func NewServer(name, version string, tools []Tool) *Server {
	s := &Server{name: name, version: version, tools: tools, byName: make(map[string]*Tool, len(tools))}
	for i := range s.tools {
		s.byName[s.tools[i].Name] = &s.tools[i]
	}
	return s
}

// Serve reads requests from in and writes responses to out until in closes or
// ctx is cancelled. Requests are handled one at a time, in order.
func (s *Server) Serve(ctx context.Context, in io.Reader, out io.Writer) error {
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 64<<10), maxMessageBytes)
	var writeMu sync.Mutex
	encoder := json.NewEncoder(out)
	write := func(value response) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		return encoder.Encode(value)
	}
	for scanner.Scan() {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		if reply, ok := s.handle(ctx, line); ok {
			if err := write(reply); err != nil {
				return err
			}
		}
	}
	return scanner.Err()
}

// handle processes one message. It returns false for notifications, which
// get no response.
func (s *Server) handle(ctx context.Context, line []byte) (response, bool) {
	var message request
	if err := json.Unmarshal(line, &message); err != nil {
		return errorReply(json.RawMessage("null"), codeParseError, "invalid JSON"), true
	}
	if message.JSONRPC != "2.0" || message.Method == "" {
		if len(message.ID) == 0 {
			return response{}, false
		}
		return errorReply(message.ID, codeInvalidRequest, "expected a JSON-RPC 2.0 request"), true
	}
	if len(message.ID) == 0 {
		return response{}, false
	}
	result, rpcErr := s.dispatch(ctx, message)
	if rpcErr != nil {
		return response{JSONRPC: "2.0", ID: message.ID, Error: rpcErr}, true
	}
	return response{JSONRPC: "2.0", ID: message.ID, Result: result}, true
}

func (s *Server) dispatch(ctx context.Context, message request) (any, *rpcError) {
	switch message.Method {
	case "initialize":
		return map[string]any{
			"protocolVersion": ProtocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]any{"name": s.name, "version": s.version},
			"instructions": "GideonDB vector database. Use search and upsert_records for collections, " +
				"and remember/recall/forget for long-term memory scoped by user.",
		}, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		return map[string]any{"tools": s.tools}, nil
	case "tools/call":
		var params struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(message.Params, &params); err != nil {
			return nil, &rpcError{Code: codeInvalidParams, Message: "invalid tools/call params"}
		}
		tool, ok := s.byName[params.Name]
		if !ok {
			return nil, &rpcError{Code: codeInvalidParams, Message: fmt.Sprintf("unknown tool %q", params.Name)}
		}
		if len(params.Arguments) == 0 {
			params.Arguments = json.RawMessage("{}")
		}
		value, err := tool.handler(ctx, params.Arguments)
		if err != nil {
			// Tool failures are reported in the result so the model can see and
			// correct them, as the MCP specification recommends.
			return toolResult(map[string]any{"error": err.Error()}, true), nil
		}
		return toolResult(value, false), nil
	default:
		return nil, &rpcError{Code: codeMethodNotFound, Message: fmt.Sprintf("method %q not found", message.Method)}
	}
}

func toolResult(value any, isError bool) map[string]any {
	text, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		text = []byte(fmt.Sprintf(`{"error":%q}`, err.Error()))
		isError = true
	}
	result := map[string]any{"content": []map[string]any{{"type": "text", "text": string(text)}}, "isError": isError}
	if !isError {
		result["structuredContent"] = structured(value)
	}
	return result
}

// structured returns value as a JSON object, wrapping non-objects, because
// MCP structuredContent must be an object.
func structured(value any) any {
	data, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	var object map[string]any
	if json.Unmarshal(data, &object) == nil {
		return object
	}
	return map[string]any{"result": json.RawMessage(data)}
}

func errorReply(id json.RawMessage, code int, message string) response {
	return response{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: message}}
}

// decode strictly parses tool arguments.
func decode(arguments json.RawMessage, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(arguments))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("invalid arguments: %w", err)
	}
	return nil
}
