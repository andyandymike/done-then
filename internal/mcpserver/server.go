package mcpserver

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/andyandymike/done-then/internal/pluginapi"
)

const latestProtocolVersion = "2025-11-25"

const maxMessageBytes = 1 << 20

var supportedProtocolVersions = map[string]bool{
	"2025-11-25": true,
	"2025-06-18": true,
	"2025-03-26": true,
}

type Server struct {
	service *pluginapi.Service
	version string
}

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

type tool struct {
	Name        string         `json:"name"`
	Title       string         `json:"title,omitempty"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

type content struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type callResult struct {
	Content           []content      `json:"content"`
	StructuredContent map[string]any `json:"structuredContent"`
	IsError           bool           `json:"isError,omitempty"`
}

func New(service *pluginapi.Service, version string) (*Server, error) {
	if service == nil {
		return nil, errors.New("plugin service is required")
	}
	if version == "" {
		version = "dev"
	}
	return &Server{service: service, version: version}, nil
}

func (s *Server) Serve(ctx context.Context, reader io.Reader, writer io.Writer) error {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), maxMessageBytes)
	encoder := json.NewEncoder(writer)
	initialized := false
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return nil
		}
		raw := json.RawMessage(bytes.TrimSpace(scanner.Bytes()))
		if len(raw) == 0 {
			continue
		}
		var message request
		if err := decodeStrict(raw, &message); err != nil {
			if writeErr := encoder.Encode(errorResponse(json.RawMessage("null"), -32700, "Parse error")); writeErr != nil {
				return fmt.Errorf("write MCP parse error: %w", writeErr)
			}
			continue
		}
		if message.JSONRPC != "2.0" || message.Method == "" {
			if len(message.ID) != 0 {
				if err := encoder.Encode(errorResponse(message.ID, -32600, "Invalid Request")); err != nil {
					return fmt.Errorf("write MCP invalid-request error: %w", err)
				}
			}
			continue
		}
		if len(message.ID) == 0 {
			if message.Method == "notifications/initialized" {
				initialized = true
			}
			continue
		}
		var reply response
		switch message.Method {
		case "initialize":
			result, err := s.initialize(message.Params)
			if err != nil {
				reply = errorResponse(message.ID, -32602, err.Error())
			} else {
				initialized = true
				reply = successResponse(message.ID, result)
			}
		case "ping":
			reply = successResponse(message.ID, map[string]any{})
		case "tools/list":
			if !initialized {
				reply = errorResponse(message.ID, -32002, "Server is not initialized")
			} else {
				reply = successResponse(message.ID, map[string]any{"tools": tools()})
			}
		case "tools/call":
			if !initialized {
				reply = errorResponse(message.ID, -32002, "Server is not initialized")
			} else {
				reply = s.call(ctx, message.ID, message.Params)
			}
		default:
			reply = errorResponse(message.ID, -32601, "Method not found")
		}
		if err := encoder.Encode(reply); err != nil {
			return fmt.Errorf("write MCP response: %w", err)
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read MCP message: %w", err)
	}
	return nil
}

func (s *Server) initialize(raw json.RawMessage) (map[string]any, error) {
	var params struct {
		ProtocolVersion string `json:"protocolVersion"`
		Capabilities    any    `json:"capabilities"`
		ClientInfo      any    `json:"clientInfo"`
		Meta            any    `json:"_meta"`
	}
	if err := decodeStrict(defaultObject(raw), &params); err != nil {
		return nil, errors.New("invalid initialize parameters")
	}
	if params.ProtocolVersion == "" {
		return nil, errors.New("initialize requires protocolVersion")
	}
	version := latestProtocolVersion
	if supportedProtocolVersions[params.ProtocolVersion] {
		version = params.ProtocolVersion
	}
	return map[string]any{
		"protocolVersion": version,
		"capabilities": map[string]any{
			"tools": map[string]any{"listChanged": false},
		},
		"serverInfo": map[string]any{
			"name":    "done-then",
			"version": s.version,
		},
		"instructions": "DoneThen defaults to after_stop dry-run. after_all_stop creates one fail-closed observation barrier across 2-16 explicit session ids. Neither Stop policy claims task success, and public Stop-based execute remains unavailable until trusted final Hook arbitration exists. verified_success is separate and also unavailable for public execute.",
	}, nil
}

func (s *Server) call(ctx context.Context, id, raw json.RawMessage) response {
	var params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
		Meta      any             `json:"_meta"`
	}
	if err := decodeStrict(defaultObject(raw), &params); err != nil || params.Name == "" {
		return errorResponse(id, -32602, "Invalid tools/call parameters")
	}
	result := s.service.Call(ctx, params.Name, defaultObject(params.Arguments))
	return successResponse(id, callResult{
		Content:           []content{{Type: "text", Text: result.Text}},
		StructuredContent: result.Structured,
		IsError:           result.IsError,
	})
}

func tools() []tool {
	stringProperty := func(description string) map[string]any {
		return map[string]any{"type": "string", "description": description}
	}
	completionSchema := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required": []string{
			"schema_version", "status", "summary", "checks", "remaining_work", "approval_required",
		},
		"properties": map[string]any{
			"schema_version": map[string]any{"type": "string", "const": "1"},
			"status": map[string]any{
				"type": "string", "enum": []string{"done", "partial", "blocked", "failed"},
			},
			"summary": map[string]any{"type": "string", "minLength": 1, "maxLength": 4096},
			"checks": map[string]any{
				"type": "array", "maxItems": 64,
				"items": map[string]any{
					"type":                 "object",
					"additionalProperties": false,
					"required":             []string{"name", "status", "evidence"},
					"properties": map[string]any{
						"name":     map[string]any{"type": "string", "minLength": 1, "maxLength": 256},
						"status":   map[string]any{"type": "string", "enum": []string{"passed", "failed", "not_run"}},
						"evidence": map[string]any{"type": "string", "maxLength": 4096},
					},
				},
			},
			"remaining_work": map[string]any{
				"type": "array", "maxItems": 64,
				"items": map[string]any{"type": "string", "minLength": 1, "maxLength": 1024},
			},
			"approval_required": map[string]any{"type": "boolean"},
		},
	}
	return []tool{
		{
			Name:        "arm",
			Title:       "Arm DoneThen",
			Description: "Create a time-limited one-shot lifecycle grant. after_stop observes one session; after_all_stop waits for every explicitly listed target session. Stop intentionally does not mean task success. Dry-run is available; execute is accepted only when the returned per-policy readiness says a trusted authority path is ready.",
			InputSchema: map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"required": []string{
					"action", "trigger_policy", "acknowledge_stop_without_success", "delay_seconds", "expires_in_seconds", "mode", "verifier_profile", "allow_agent_only_success",
				},
				"properties": map[string]any{
					"action": map[string]any{"type": "string", "const": "shutdown"},
					"trigger_policy": map[string]any{
						"type": "string", "enum": []string{"after_stop", "after_all_stop", "verified_success"},
						"description": "Use after_all_stop only when the user explicitly supplies 2-16 target session ids; neither Stop policy proves success",
					},
					"acknowledge_stop_without_success": map[string]any{
						"type":        "boolean",
						"description": "Must be true for after_stop or after_all_stop execute; acknowledges that a normal Stop after partial, blocked, or question-ending work can trigger the countdown",
					},
					"target_session_ids": map[string]any{
						"type": "array", "minItems": 2, "maxItems": 16, "uniqueItems": true,
						"items":       map[string]any{"type": "string", "minLength": 1, "maxLength": 1024},
						"description": "after_all_stop only: exact opaque Codex session ids in user-supplied order",
					},
					"acknowledge_barrier_across_turns": map[string]any{
						"type":        "boolean",
						"description": "after_all_stop execute only: acknowledges that a target which resumes before countdown remains pending until a later Stop",
					},
					"delay_seconds":      map[string]any{"type": "integer", "minimum": 30, "maximum": 3600},
					"expires_in_seconds": map[string]any{"type": "integer", "minimum": 60, "maximum": 86400},
					"mode":               map[string]any{"type": "string", "enum": []string{"dry_run", "execute"}},
					"verifier_profile":   stringProperty("Use none for after_stop; a pre-registered verifier id is only for verified_success"),
					"allow_agent_only_success": map[string]any{
						"type":        "boolean",
						"description": "verified_success only: explicitly accept structured agent self-report when verifier_profile is none",
					},
				},
			},
		},
		{
			Name:        "finish",
			Title:       "Report DoneThen completion",
			Description: "verified_success only: validate structured completion and run the fixed registered verifier. after_stop never calls this tool.",
			InputSchema: map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []string{"job_id", "completion"},
				"properties": map[string]any{
					"job_id":     stringProperty("DoneThen one-shot job id"),
					"completion": completionSchema,
				},
			},
		},
		{
			Name:        "pause",
			Title:       "Pause DoneThen",
			Description: "verified_success only: invalidate completion evidence while the task waits. Cancel after_stop instead of pausing it.",
			InputSchema: map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []string{"job_id", "reason"},
				"properties": map[string]any{
					"job_id": stringProperty("DoneThen one-shot job id"),
					"reason": map[string]any{"type": "string", "enum": []string{"blocked", "approval_required", "waiting_for_user", "external_state"}},
				},
			},
		},
		{
			Name:        "cancel",
			Title:       "Cancel DoneThen",
			Description: "Idempotently disarm a DoneThen job and, when a receipt exists, cancel only through the receipt-bound platform backend.",
			InputSchema: jobIDSchema(true),
		},
		{
			Name:        "status",
			Title:       "Read DoneThen status",
			Description: "Return redacted plugin job state, hook compatibility, expiry, and cancellation guidance.",
			InputSchema: jobIDSchema(false),
		},
	}
}

func jobIDSchema(required bool) map[string]any {
	schema := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"job_id": map[string]any{"type": "string", "description": "DoneThen one-shot job id"},
		},
	}
	if required {
		schema["required"] = []string{"job_id"}
	}
	return schema
}

func defaultObject(raw json.RawMessage) json.RawMessage {
	if len(bytes.TrimSpace(raw)) == 0 || string(bytes.TrimSpace(raw)) == "null" {
		return json.RawMessage("{}")
	}
	return raw
}

func decodeStrict(raw json.RawMessage, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON")
	}
	return nil
}

func successResponse(id json.RawMessage, result any) response {
	return response{JSONRPC: "2.0", ID: id, Result: result}
}

func errorResponse(id json.RawMessage, code int, message string) response {
	return response{
		JSONRPC: "2.0",
		ID:      id,
		Error:   &rpcError{Code: code, Message: message},
	}
}
