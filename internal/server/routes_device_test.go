package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jbringb/puls/internal/auth"
)

func TestHandleAdminToken(t *testing.T) {
	s := newTestServer(t)

	tests := []struct {
		name       string
		body       string
		wantStatus int
	}{
		{"valid secret", `{"secret":"` + testAdminSecret + `"}`, http.StatusOK},
		{"wrong secret", `{"secret":"nope-wrong-secret-here"}`, http.StatusUnauthorized},
		{"jwt secret is not the admin secret", `{"secret":"` + testJWTSecret + `"}`, http.StatusUnauthorized},
		{"empty secret", `{"secret":""}`, http.StatusBadRequest},
		{"unknown field rejected", `{"secret":"` + testAdminSecret + `","x":1}`, http.StatusBadRequest},
		{"malformed json", `{`, http.StatusBadRequest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/admin-token", strings.NewReader(tt.body))
			rec := httptest.NewRecorder()
			s.handleAdminToken(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (body %q)", rec.Code, tt.wantStatus, rec.Body.String())
			}
			if tt.wantStatus != http.StatusOK {
				return
			}

			var resp struct {
				Token string `json:"token"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			claims, err := s.jwtMgr.Validate(resp.Token)
			if err != nil {
				t.Fatalf("issued token does not validate: %v", err)
			}
			if claims.Role != auth.RoleAdmin {
				t.Errorf("issued token role = %q, want admin", claims.Role)
			}
		})
	}
}

func TestHandleRegister(t *testing.T) {
	s := newTestServer(t)

	tests := []struct {
		name       string
		body       string
		wantStatus int
	}{
		{"valid", `{"name":"box","os":"linux","arch":"amd64","secret":"registration-secret"}`, http.StatusCreated},
		{"invalid os", `{"name":"box","os":"plan9","arch":"amd64","secret":"registration-secret"}`, http.StatusUnprocessableEntity},
		{"short secret", `{"name":"box","os":"linux","arch":"amd64","secret":"tooshort"}`, http.StatusUnprocessableEntity},
		{"unknown field", `{"name":"box","os":"linux","arch":"amd64","secret":"registration-secret","rogue":true}`, http.StatusBadRequest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/v1/devices/register", strings.NewReader(tt.body))
			rec := httptest.NewRecorder()
			s.handleRegister(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (body %q)", rec.Code, tt.wantStatus, rec.Body.String())
			}
			if tt.wantStatus != http.StatusCreated {
				return
			}

			var resp struct {
				DeviceID string `json:"deviceId"`
				Token    string `json:"token"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if resp.DeviceID == "" {
				t.Error("deviceId is empty")
			}
			claims, err := s.jwtMgr.Validate(resp.Token)
			if err != nil {
				t.Fatalf("issued device token does not validate: %v", err)
			}
			if claims.Role != auth.RoleDevice {
				t.Errorf("issued token role = %q, want device", claims.Role)
			}
		})
	}
}
