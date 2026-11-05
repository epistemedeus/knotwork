package logging

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"
)

type contextKey struct{}

var requestIDKey = contextKey{}

// WithRequestID returns a new context with the given request ID.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey, id)
}

// RequestIDFromContext extracts the request ID from context if present.
func RequestIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if id, ok := ctx.Value(requestIDKey).(string); ok {
		return id
	}
	return ""
}

// Config specifies logger configuration.
type Config struct {
	Level     string // "debug", "info", "warn", "error"
	Format    string // "text", "json"
	AddSource bool   // if true, file and line number are included
}

// NewLogger creates a configured *slog.Logger.
func NewLogger(cfg Config, out io.Writer) *slog.Logger {
	if out == nil {
		out = os.Stderr
	}

	var level slog.Level
	switch strings.ToLower(strings.TrimSpace(cfg.Level)) {
	case "debug":
		level = slog.LevelDebug
	case "warn", "warning":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}

	addSource := cfg.AddSource || level == slog.LevelDebug

	opts := &slog.HandlerOptions{
		Level:     level,
		AddSource: addSource,
	}

	var handler slog.Handler
	if strings.ToLower(strings.TrimSpace(cfg.Format)) == "json" {
		handler = slog.NewJSONHandler(out, opts)
	} else {
		handler = slog.NewTextHandler(out, opts)
	}

	return slog.New(&contextHandler{Handler: handler})
}

type contextHandler struct {
	slog.Handler
}

func (h *contextHandler) Handle(ctx context.Context, r slog.Record) error {
	if reqID := RequestIDFromContext(ctx); reqID != "" {
		r.Add("req_id", reqID)
	}
	return h.Handler.Handle(ctx, r)
}

// GenerateRequestID creates a random 16-hex character request ID.
func GenerateRequestID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "0000000000000000"
	}
	return hex.EncodeToString(b)
}

type responseRecorder struct {
	http.ResponseWriter
	status       int
	bytesWritten int64
}

func (r *responseRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *responseRecorder) Write(b []byte) (int, error) {
	n, err := r.ResponseWriter.Write(b)
	r.bytesWritten += int64(n)
	return n, err
}

func (r *responseRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Middleware returns an HTTP middleware that injects X-Request-ID and logs
// structured HTTP request access logs.
func Middleware(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()

			reqID := r.Header.Get("X-Request-ID")
			if reqID == "" {
				reqID = GenerateRequestID()
			}
			w.Header().Set("X-Request-ID", reqID)

			ctx := WithRequestID(r.Context(), reqID)
			r = r.WithContext(ctx)

			rec := &responseRecorder{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(rec, r)

			remote := r.Header.Get("X-Forwarded-For")
			if remote == "" {
				remote = r.RemoteAddr
			}

			attrs := []any{
				"method", r.Method,
				"path", r.URL.Path,
				"status", rec.status,
				"duration", time.Since(start),
				"bytes", rec.bytesWritten,
				"remote", remote,
			}
			if ua := r.UserAgent(); ua != "" {
				attrs = append(attrs, "user_agent", ua)
			}
			if r.URL.RawQuery != "" {
				attrs = append(attrs, "query", r.URL.RawQuery)
			}

			switch {
			case rec.status >= 500:
				log.ErrorContext(ctx, "http request", attrs...)
			case rec.status >= 400:
				log.WarnContext(ctx, "http request", attrs...)
			default:
				log.InfoContext(ctx, "http request", attrs...)
			}
		})
	}
}
