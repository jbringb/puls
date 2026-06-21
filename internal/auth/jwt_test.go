package auth

import (
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const testSecret = "test-signing-secret-at-least-32-chars!"

func TestNewManagerRejectsShortSecret(t *testing.T) {
	if _, err := NewManager("too-short"); err == nil {
		t.Fatal("NewManager() with short secret: want error, got nil")
	}
	if _, err := NewManager(testSecret); err != nil {
		t.Fatalf("NewManager() with valid secret: %v", err)
	}
}

func TestIssueValidateRoundTrip(t *testing.T) {
	m, err := NewManager(testSecret)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name     string
		issue    func() (string, error)
		wantRole Role
		wantSub  string
	}{
		{
			name:     "device token",
			issue:    func() (string, error) { return m.IssueDeviceToken("device-123", time.Hour) },
			wantRole: RoleDevice,
			wantSub:  "device-123",
		},
		{
			name:     "admin token",
			issue:    func() (string, error) { return m.IssueAdminToken("admin", time.Hour) },
			wantRole: RoleAdmin,
			wantSub:  "admin",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tok, err := tt.issue()
			if err != nil {
				t.Fatalf("issue: %v", err)
			}
			claims, err := m.Validate(tok)
			if err != nil {
				t.Fatalf("Validate: %v", err)
			}
			if claims.Role != tt.wantRole {
				t.Errorf("Role = %q, want %q", claims.Role, tt.wantRole)
			}
			if claims.Subject != tt.wantSub {
				t.Errorf("Subject = %q, want %q", claims.Subject, tt.wantSub)
			}
		})
	}
}

func TestValidateRejectsExpiredToken(t *testing.T) {
	m, _ := NewManager(testSecret)
	tok, err := m.IssueDeviceToken("device-123", -time.Hour) // already expired
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Validate(tok); err == nil {
		t.Fatal("Validate() expired token: want error, got nil")
	}
}

func TestValidateRejectsWrongSecret(t *testing.T) {
	m, _ := NewManager(testSecret)
	other, _ := NewManager("a-totally-different-secret-32-chars!!")
	tok, _ := other.IssueAdminToken("admin", time.Hour)
	if _, err := m.Validate(tok); err == nil {
		t.Fatal("Validate() token from foreign secret: want error, got nil")
	}
}

func TestValidateRejectsNoneAlg(t *testing.T) {
	m, _ := NewManager(testSecret)
	// Forge an unsigned token; the validator must reject non-HMAC methods.
	tok := jwt.NewWithClaims(jwt.SigningMethodNone, Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   "admin",
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
		},
		Role: RoleAdmin,
	})
	signed, err := tok.SignedString(jwt.UnsafeAllowNoneSignatureType)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Validate(signed); err == nil {
		t.Fatal("Validate() alg=none token: want error, got nil")
	}
}
