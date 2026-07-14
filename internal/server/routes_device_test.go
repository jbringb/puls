package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jbringb/puls/internal/auth"
	"github.com/jbringb/puls/internal/model"
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

func TestHandleListDevicesPagination(t *testing.T) {
	s := newTestServer(t)
	ctx := context.Background()

	const total = 5
	ids := make(map[string]bool, total)
	for i := 0; i < total; i++ {
		d, err := s.store.CreateDevice(ctx, &model.RegisterRequest{
			Name: fmt.Sprintf("box-%d", i), OS: model.OSLinux, Arch: "amd64", Secret: "registration-secret",
		})
		if err != nil {
			t.Fatalf("CreateDevice: %v", err)
		}
		ids[d.ID] = true
	}

	seen := make(map[string]bool, total)
	url := "/api/v1/devices?limit=2"
	for pages := 0; ; pages++ {
		if pages > total {
			t.Fatalf("pagination did not terminate after %d pages", pages)
		}
		req := httptest.NewRequest(http.MethodGet, url, nil)
		rec := httptest.NewRecorder()
		s.handleListDevices(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
		}
		var page model.DeviceList
		if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if len(page.Devices) > 2 {
			t.Fatalf("page returned %d devices, want at most 2", len(page.Devices))
		}
		for _, d := range page.Devices {
			if seen[d.ID] {
				t.Fatalf("device %s returned twice across pages", d.ID)
			}
			seen[d.ID] = true
		}
		if page.NextCursor == "" {
			break
		}
		url = "/api/v1/devices?limit=2&cursor=" + page.NextCursor
	}

	if len(seen) != total {
		t.Fatalf("saw %d devices across pages, want %d", len(seen), total)
	}
	for id := range ids {
		if !seen[id] {
			t.Errorf("device %s never appeared in any page", id)
		}
	}
}

func TestHandleListDevicesInvalidQuery(t *testing.T) {
	s := newTestServer(t)

	tests := []struct {
		name string
		url  string
	}{
		{"non-numeric limit", "/api/v1/devices?limit=abc"},
		{"zero limit", "/api/v1/devices?limit=0"},
		{"negative limit", "/api/v1/devices?limit=-1"},
		{"malformed cursor", "/api/v1/devices?cursor=not-valid-base64!!"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.url, nil)
			rec := httptest.NewRecorder()
			s.handleListDevices(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body %q)", rec.Code, rec.Body.String())
			}
		})
	}
}
