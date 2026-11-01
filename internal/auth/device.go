package auth

import (
	"encoding/json"
	"errors"
	"html/template"
	"net/http"
	"strings"
	"time"

	"knotwork/internal/database"
)

const (
	deviceCodeTTL = 10 * time.Minute
	devicePoll    = 5
)

type deviceCodeResponse struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

func (s *Service) deviceCode(w http.ResponseWriter, r *http.Request) {
	deviceCode, userCode, err := s.DB.CreateDeviceCode(r.Context(), deviceCodeTTL)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "could not create device code")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(deviceCodeResponse{
		DeviceCode:              deviceCode,
		UserCode:                userCode,
		VerificationURI:         s.Cfg.BaseURL + "/auth/device",
		VerificationURIComplete: s.Cfg.BaseURL + "/auth/device?code=" + userCode,
		ExpiresIn:               int(deviceCodeTTL.Seconds()),
		Interval:                devicePoll,
	})
}

func (s *Service) deviceToken(w http.ResponseWriter, r *http.Request) {
	deviceCode := r.FormValue("device_code")
	if deviceCode == "" {
		var body struct {
			DeviceCode string `json:"device_code"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		deviceCode = body.DeviceCode
	}
	if deviceCode == "" {
		writeDeviceError(w, "invalid_request", "device_code is required")
		return
	}
	status, err := s.DB.DeviceCodeStatus(r.Context(), deviceCode)
	if errors.Is(err, database.ErrNotFound) {
		writeDeviceError(w, "expired_token", "device code is unknown or expired")
		return
	}
	if err != nil {
		writeDeviceError(w, "server_error", "could not read device code")
		return
	}
	if status != "approved" {
		writeDeviceError(w, "authorization_pending", "the user has not approved this device yet")
		return
	}
	user, err := s.DB.ConsumeDeviceCode(r.Context(), deviceCode)
	if err != nil || !user.IsActive {
		writeDeviceError(w, "access_denied", "the user is not allowed to sign in")
		return
	}
	token, err := s.Tokens.Issue(user.ID, user.Username, user.Role, []string{"git:write", "api"})
	if err != nil {
		writeDeviceError(w, "server_error", "could not issue token")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"access_token": token,
		"token_type":   "Bearer",
		"expires_in":   int(s.Tokens.TTL().Seconds()),
		"scope":        "git:write api",
	})
}

func (s *Service) devicePage(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("code")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = deviceTemplate.Execute(w, map[string]string{"Code": code})
}

func (s *Service) deviceApprove(w http.ResponseWriter, r *http.Request) {
	user, _ := UserFromContext(r.Context())
	code := strings.ToUpper(strings.TrimSpace(r.FormValue("code")))
	ok, err := s.DB.ApproveDeviceCode(r.Context(), code, user.ID)
	if err != nil {
		http.Error(w, "could not approve device", http.StatusInternalServerError)
		return
	}
	if !ok {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusBadRequest)
		_ = approvedTemplate.Execute(w, map[string]string{"Message": "That code is invalid or has expired. Run the login command again."})
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = approvedTemplate.Execute(w, map[string]string{"Message": "Device approved. You can return to your terminal."})
}

func writeDeviceError(w http.ResponseWriter, code, description string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusBadRequest)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code, "error_description": description})
}

var deviceTemplate = template.Must(template.New("device").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>Authorize Knotwork CLI</title>
<style>body{font-family:system-ui,sans-serif;max-width:30rem;margin:4rem auto;padding:0 1rem}input,button{font:inherit;padding:.5rem}input{width:12rem;text-transform:uppercase}</style></head>
<body><h1>Authorize a terminal</h1>
<p>Enter the code shown by <code>knotwork auth login</code>.</p>
<form method="post" action="/auth/device">
<input name="code" value="{{.Code}}" autocomplete="one-time-code" required>
<button type="submit">Authorize</button>
</form></body></html>`))

var approvedTemplate = template.Must(template.New("approved").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>Knotwork</title></head>
<body style="font-family:system-ui,sans-serif;max-width:30rem;margin:4rem auto;padding:0 1rem">
<h1>Knotwork</h1><p>{{.Message}}</p></body></html>`))
