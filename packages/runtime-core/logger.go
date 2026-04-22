package runtimecore

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"
)

type LogContext struct {
	TraceID       string `json:"trace_id,omitempty"`
	EventID       string `json:"event_id,omitempty"`
	PluginID      string `json:"plugin_id,omitempty"`
	RunID         string `json:"run_id,omitempty"`
	CorrelationID string `json:"correlation_id,omitempty"`
}

type LogEntry struct {
	Timestamp     string         `json:"timestamp"`
	Level         string         `json:"level"`
	Message       string         `json:"message"`
	TraceID       string         `json:"trace_id,omitempty"`
	EventID       string         `json:"event_id,omitempty"`
	PluginID      string         `json:"plugin_id,omitempty"`
	RunID         string         `json:"run_id,omitempty"`
	CorrelationID string         `json:"correlation_id,omitempty"`
	Fields        map[string]any `json:"fields,omitempty"`
}

type Logger struct {
	writer io.Writer
	now    func() time.Time
}

func NewLogger(writer io.Writer) *Logger {
	return &Logger{writer: writer, now: time.Now().UTC}
}

func BaselineLogFields(component, operation string, fields map[string]any) map[string]any {
	normalized := cloneLogFields(fields)
	if normalized == nil {
		normalized = map[string]any{}
	}
	putLogField(normalized, "component", component)
	putLogField(normalized, "operation", operation)
	normalizeLogErrorFields(normalized)
	if len(normalized) == 0 {
		return nil
	}
	return normalized
}

func FailureLogFields(component, operation string, err error, fallbackCode string, fields map[string]any) map[string]any {
	normalized := cloneLogFields(fields)
	if normalized == nil {
		normalized = map[string]any{}
	}
	if err != nil && strings.TrimSpace(logFieldString(normalized, "error")) == "" {
		normalized["error"] = err.Error()
	}
	if strings.TrimSpace(logFieldString(normalized, "error_code")) == "" {
		if code := classifyLogErrorCode(err, fallbackCode, normalized); code != "" {
			normalized["error_code"] = code
		}
	}
	return BaselineLogFields(component, operation, normalized)
}

func (l *Logger) Log(level, message string, ctx LogContext, fields map[string]any) error {
	entry := LogEntry{
		Timestamp:     l.now().Format(time.RFC3339),
		Level:         level,
		Message:       message,
		TraceID:       ctx.TraceID,
		EventID:       ctx.EventID,
		PluginID:      ctx.PluginID,
		RunID:         ctx.RunID,
		CorrelationID: ctx.CorrelationID,
		Fields:        normalizeWritableLogFields(fields),
	}

	encoded, err := json.Marshal(entry)
	if err != nil {
		return err
	}

	_, err = l.writer.Write(append(encoded, '\n'))
	return err
}

func normalizeWritableLogFields(fields map[string]any) map[string]any {
	normalized := cloneLogFields(fields)
	normalizeLogErrorFields(normalized)
	if len(normalized) == 0 {
		return nil
	}
	return normalized
}

func cloneLogFields(fields map[string]any) map[string]any {
	if len(fields) == 0 {
		return nil
	}
	cloned := make(map[string]any, len(fields))
	for key, value := range fields {
		cloned[key] = value
	}
	return cloned
}

func putLogField(fields map[string]any, key, value string) {
	if len(fields) == 0 || strings.TrimSpace(value) == "" {
		return
	}
	if strings.TrimSpace(logFieldString(fields, key)) != "" {
		return
	}
	fields[key] = value
}

func normalizeLogErrorFields(fields map[string]any) {
	if len(fields) == 0 {
		return
	}
	code := strings.TrimSpace(logFieldString(fields, "error_code"))
	if code == "" {
		code = strings.TrimSpace(logFieldString(fields, "failure_reason"))
	}
	if code == "" {
		code = strings.TrimSpace(logFieldString(fields, "failure_code"))
	}
	if code != "" {
		fields["error_code"] = code
	}
	if strings.TrimSpace(logFieldString(fields, "error_category")) != "" {
		return
	}
	if category := inferLogErrorCategory(code, logFieldString(fields, "error")); category != "" {
		fields["error_category"] = category
	}
}

func logFieldString(fields map[string]any, key string) string {
	if len(fields) == 0 {
		return ""
	}
	value, ok := fields[key]
	if !ok || value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return typed
	case []byte:
		return string(typed)
	default:
		return ""
	}
}

func classifyLogErrorCode(err error, fallbackCode string, fields map[string]any) string {
	if code := strings.TrimSpace(logFieldString(fields, "error_code")); code != "" {
		return code
	}
	if code := strings.TrimSpace(logFieldString(fields, "failure_reason")); code != "" {
		return code
	}
	if code := strings.TrimSpace(logFieldString(fields, "failure_code")); code != "" {
		return code
	}
	if err == nil {
		return fallbackCode
	}
	if errors.Is(err, context.Canceled) {
		switch {
		case strings.Contains(fallbackCode, "dispatch"):
			return "dispatch_canceled"
		case strings.Contains(fallbackCode, "secret"):
			return "secret_read_canceled"
		default:
			return "context_canceled"
		}
	}
	lower := strings.ToLower(strings.TrimSpace(err.Error()))
	switch {
	case errors.Is(err, context.DeadlineExceeded), strings.Contains(lower, "subprocess response timeout"):
		return "response_timeout"
	case strings.Contains(lower, "timeout"):
		return "timeout"
	case strings.Contains(lower, "plugin scope denied"):
		return "plugin_scope_denied"
	case strings.Contains(lower, "permission denied"), strings.Contains(lower, "authorization denied"):
		return "permission_denied"
	case strings.Contains(lower, "launch guard"):
		return "launcher_guard_blocked"
	case strings.Contains(lower, "dispatch failed after handshake"), strings.Contains(lower, "crash after handshake"), strings.Contains(lower, "file already closed"):
		return "crash_after_handshake"
	case strings.Contains(lower, "no plugins registered for dispatch"):
		return "dispatch_no_plugins"
	case strings.Contains(lower, "no successful plugin deliveries"):
		return "dispatch_no_deliveries"
	default:
		return fallbackCode
	}
}

func inferLogErrorCategory(code string, errorText string) string {
	switch strings.TrimSpace(code) {
	case "permission_denied", "plugin_scope_denied", "webhook_unauthorized":
		return "authorization"
	case "missing_manifest_permission",
		"webhook_invalid_payload",
		"webhook_invalid_event",
		"invalid_secret_ref",
		"manifest_mode_mismatch",
		"manifest_unsupported_api_version",
		"manifest_unsupported_runtime_version",
		"manifest_missing_entry_target",
		"manifest_invalid_config_schema",
		"manifest_missing_required_config",
		"manifest_invalid_config_property",
		"instance_config_missing_required_value",
		"instance_config_value_type_mismatch",
		"instance_config_enum_value_out_of_set":
		return "validation"
	case "dispatch_failed",
		"secret_resolution_failed",
		"secret_not_found",
		"secret_read_failed",
		"plugin_supervisor_failed",
		"plugin_dispatch_failed",
		"ensure_process_failed",
		"crash_after_handshake",
		"launcher_guard_blocked":
		return "dependency"
	case "response_timeout", "timeout":
		return "timeout"
	case "dispatch_canceled", "secret_read_canceled", "context_canceled", "canceled":
		return "canceled"
	case "dispatch_no_plugins",
		"dispatch_no_deliveries",
		"dispatch_snapshot_persist_failed",
		"queued_dispatch_failed",
		"claim_ready_failed",
		"health_check_failed",
		"read_failed",
		"write_failed":
		return "internal"
	}
	lower := strings.ToLower(strings.TrimSpace(errorText))
	switch {
	case lower == "":
		return ""
	case strings.Contains(lower, "permission denied"), strings.Contains(lower, "authorization denied"):
		return "authorization"
	case strings.Contains(lower, "timeout"):
		return "timeout"
	case strings.Contains(lower, "canceled") || strings.Contains(lower, "cancelled"):
		return "canceled"
	default:
		return "internal"
	}
}
