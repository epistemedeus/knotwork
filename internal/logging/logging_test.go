package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNewLoggerLevelsAndFormats(t *testing.T) {
	t.Run("json format with debug level", func(t *testing.T) {
		var buf bytes.Buffer
		logger := NewLogger(Config{Level: "debug", Format: "json"}, &buf)

		logger.Debug("debug message", "key", "val")

		var entry map[string]any
		if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
			t.Fatalf("json unmarshal: %v\noutput: %s", err, buf.String())
		}
		if entry["level"] != "DEBUG" {
			t.Errorf("level = %v, want DEBUG", entry["level"])
		}
		if entry["msg"] != "debug message" {
			t.Errorf("msg = %v, want 'debug message'", entry["msg"])
		}
		if entry["key"] != "val" {
			t.Errorf("key = %v, want 'val'", entry["key"])
		}
		if entry["source"] == nil {
			t.Error("expected source location in debug mode")
		}
	})

	t.Run("text format with info level filters debug", func(t *testing.T) {
		var buf bytes.Buffer
		logger := NewLogger(Config{Level: "info", Format: "text"}, &buf)

		logger.Debug("should be ignored")
		if buf.Len() != 0 {
			t.Errorf("expected no output for debug in info level, got %s", buf.String())
		}

		logger.Info("info message")
		if !strings.Contains(buf.String(), "info message") {
			t.Errorf("expected info message in output, got %s", buf.String())
		}
	})

	t.Run("contextual request id injection", func(t *testing.T) {
		var buf bytes.Buffer
		logger := NewLogger(Config{Level: "info", Format: "json"}, &buf)

		ctx := WithRequestID(context.Background(), "req-xyz-123")
		logger.InfoContext(ctx, "authenticated request", "user", "alice")

		var entry map[string]any
		if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
			t.Fatalf("json unmarshal: %v", err)
		}
		if entry["req_id"] != "req-xyz-123" {
			t.Errorf("req_id = %v, want req-xyz-123", entry["req_id"])
		}
		if entry["user"] != "alice" {
			t.Errorf("user = %v, want alice", entry["user"])
		}
	})
}

func TestMiddleware(t *testing.T) {
	var buf bytes.Buffer
	logger := NewLogger(Config{Level: "info", Format: "json"}, &buf)
	mw := Middleware(logger)

	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqID := RequestIDFromContext(r.Context())
		if reqID == "" {
			t.Error("request ID missing from context inside handler")
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("hello world"))
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), "GET", "/test/path?foo=bar", http.NoBody)
	req.Header.Set("User-Agent", "TestClient/1.0")

	handler.ServeHTTP(rec, req)

	respReqID := rec.Header().Get("X-Request-ID")
	if respReqID == "" {
		t.Error("X-Request-ID missing from response headers")
	}

	var entry map[string]any
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("json unmarshal: %v\noutput: %s", err, buf.String())
	}
	if entry["path"] != "/test/path" {
		t.Errorf("path = %v, want /test/path", entry["path"])
	}
	if entry["status"] != float64(200) {
		t.Errorf("status = %v, want 200", entry["status"])
	}
	if entry["bytes"] != float64(11) {
		t.Errorf("bytes = %v, want 11", entry["bytes"])
	}
	if entry["query"] != "foo=bar" {
		t.Errorf("query = %v, want foo=bar", entry["query"])
	}
	if entry["req_id"] != respReqID {
		t.Errorf("req_id in log (%v) != response header (%v)", entry["req_id"], respReqID)
	}
}
