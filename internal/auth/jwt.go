package auth

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

type Role string

const (
	RoleDevice Role = "device"
	RoleAdmin  Role = "admin"
)

type Claims struct {
	jwt.RegisteredClaims
	Role Role `json:"role"`
}

type Manager struct {
	secret []byte
}

func NewManager(secret string) (*Manager, error) {
	if len(secret) < 32 {
		return nil, errors.New("auth: JWT secret must be at least 32 characters")
	}
	return &Manager{secret: []byte(secret)}, nil
}

func (m *Manager) IssueDeviceToken(deviceID string, expiry time.Duration) (string, error) {
	return m.issue(deviceID, RoleDevice, expiry)
}

func (m *Manager) IssueAdminToken(subject string, expiry time.Duration) (string, error) {
	return m.issue(subject, RoleAdmin, expiry)
}

func (m *Manager) issue(subject string, role Role, expiry time.Duration) (string, error) {
	now := time.Now()
	claims := Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   subject,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(expiry)),
		},
		Role: role,
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString(m.secret)
	if err != nil {
		return "", fmt.Errorf("auth: sign token: %w", err)
	}
	return signed, nil
}

func (m *Manager) Validate(tokenStr string) (*Claims, error) {
	token, err := jwt.ParseWithClaims(tokenStr, &Claims{}, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("auth: unexpected signing method: %v", t.Header["alg"])
		}
		return m.secret, nil
	})
	if err != nil {
		return nil, fmt.Errorf("auth: validate token: %w", err)
	}

	claims, ok := token.Claims.(*Claims)
	if !ok || !token.Valid {
		return nil, errors.New("auth: invalid token claims")
	}
	return claims, nil
}
