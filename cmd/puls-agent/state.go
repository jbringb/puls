package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// state is the persisted registration — saved so restarts reconnect as the
// same device instead of registering a fresh one every run.
type state struct {
	DeviceID string `json:"deviceId"`
	Token    string `json:"token"`
}

func loadState(path string) (*state, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read state file: %w", err)
	}
	var s state
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("parse state file: %w", err)
	}
	if s.DeviceID == "" || s.Token == "" {
		return nil, nil
	}
	return &s, nil
}

func saveState(path string, s *state) error {
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("create state dir: %w", err)
		}
	}
	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("encode state: %w", err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		return fmt.Errorf("write state file: %w", err)
	}
	return nil
}

func clearState(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove state file: %w", err)
	}
	return nil
}
