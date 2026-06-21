package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWSToken(t *testing.T) {
	tests := []struct {
		name      string
		setup     func(r *http.Request)
		wantToken string
	}{
		{
			name:      "authorization header preferred",
			setup:     func(r *http.Request) { r.Header.Set("Authorization", "Bearer abc.def") },
			wantToken: "abc.def",
		},
		{
			name:      "subprotocol sentinel",
			setup:     func(r *http.Request) { r.Header.Set("Sec-WebSocket-Protocol", "puls.bearer, jwt.token.here") },
			wantToken: "jwt.token.here",
		},
		{
			name:      "query fallback",
			setup:     func(r *http.Request) { r.URL.RawQuery = "token=from.query" },
			wantToken: "from.query",
		},
		{
			name: "header wins over query",
			setup: func(r *http.Request) {
				r.Header.Set("Authorization", "Bearer from.header")
				r.URL.RawQuery = "token=from.query"
			},
			wantToken: "from.header",
		},
		{
			name:      "subprotocol without trailing token",
			setup:     func(r *http.Request) { r.Header.Set("Sec-WebSocket-Protocol", "puls.bearer") },
			wantToken: "",
		},
		{
			name:      "no token",
			setup:     func(r *http.Request) {},
			wantToken: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/ws", nil)
			tt.setup(req)
			if got := wsToken(req); got != tt.wantToken {
				t.Fatalf("wsToken() = %q, want %q", got, tt.wantToken)
			}
		})
	}
}
