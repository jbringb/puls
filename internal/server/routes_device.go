package server

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/jbringb/puls/internal/model"
	"github.com/jbringb/puls/internal/store"
)

func (s *Server) handleAdminToken(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Secret string `json:"secret"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil || body.Secret == "" {
		writeError(w, http.StatusBadRequest, "secret required")
		return
	}
	// Constant-time compare against the dedicated admin secret — never the JWT
	// signing key, which would let an admin forge arbitrary tokens directly.
	if subtle.ConstantTimeCompare([]byte(body.Secret), []byte(s.cfg.AdminSecret)) != 1 {
		writeError(w, http.StatusUnauthorized, "invalid secret")
		return
	}
	token, err := s.jwtMgr.IssueAdminToken("admin", s.cfg.AdminTokenExpiry)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to issue token")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"token": token})
}

func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	var req model.RegisterRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}

	if err := validateRegisterRequest(&req); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	ctx := r.Context()
	device, err := s.store.CreateDevice(ctx, &req)
	if err != nil {
		s.logger.Error("create device", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to register device")
		return
	}

	token, err := s.jwtMgr.IssueDeviceToken(device.ID, s.cfg.DeviceTokenExpiry)
	if err != nil {
		s.logger.Error("issue token", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to issue token")
		return
	}

	writeJSON(w, http.StatusCreated, model.RegisterResponse{
		DeviceID: device.ID,
		Token:    token,
	})
}

func (s *Server) handleListDevices(w http.ResponseWriter, r *http.Request) {
	devices, err := s.store.ListDevices(r.Context())
	if err != nil {
		s.logger.Error("list devices", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to list devices")
		return
	}

	// Overlay live connection status from the hub on top of the DB value.
	connectedIDs := s.hub.ConnectedDeviceIDs()
	connected := make(map[string]bool, len(connectedIDs))
	for i := 0; i < len(connectedIDs); i++ {
		connected[connectedIDs[i]] = true
	}
	for i := range devices {
		if connected[devices[i].ID] {
			devices[i].Status = model.StatusOnline
		}
	}

	writeJSON(w, http.StatusOK, devices)
}

func (s *Server) handleGetDevice(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	ctx := r.Context()
	device, err := s.store.GetDevice(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "device not found")
		return
	}
	if err != nil {
		s.logger.Error("get device", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to get device")
		return
	}

	heartbeats, err := s.store.ListHeartbeats(ctx, id, 20)
	if err != nil {
		s.logger.Warn("list heartbeats", "err", err)
	}

	if s.hub.IsConnected(id) {
		device.Status = model.StatusOnline
	}

	writeJSON(w, http.StatusOK, model.DeviceDetail{
		Device:           *device,
		RecentHeartbeats: heartbeats,
	})
}

func validateRegisterRequest(req *model.RegisterRequest) error {
	req.Name = strings.TrimSpace(req.Name)
	req.Secret = strings.TrimSpace(req.Secret)

	if req.Name == "" {
		return errors.New("name is required")
	}
	if req.OS != model.OSWindows && req.OS != model.OSLinux {
		return errors.New("os must be 'windows' or 'linux'")
	}
	if req.Arch == "" {
		return errors.New("arch is required")
	}
	if len(req.Secret) < 16 {
		return errors.New("secret must be at least 16 characters")
	}
	return nil
}
