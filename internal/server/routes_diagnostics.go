package server

import (
	"encoding/json"
	"net/http"

	"github.com/google/uuid"

	"github.com/jbringb/puls/internal/model"
	ws "github.com/jbringb/puls/internal/ws"
)

func (s *Server) handleRequestDiagnostics(w http.ResponseWriter, r *http.Request) {
	deviceID := r.PathValue("id")

	if !s.hub.IsConnected(deviceID) {
		writeError(w, http.StatusServiceUnavailable, "device is not connected")
		return
	}

	var body struct {
		Scope model.DiagnosticScope `json:"scope"`
	}
	body.Scope = model.ScopeFull

	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}

	switch body.Scope {
	case model.ScopeFull, model.ScopeNetwork, model.ScopeProcesses, model.ScopeStorage:
		// valid
	default:
		writeError(w, http.StatusUnprocessableEntity, "invalid scope")
		return
	}

	ctx := r.Context()
	requestID := uuid.New().String()

	result, err := s.store.CreateDiagnosticRequest(ctx, deviceID, requestID, body.Scope)
	if err != nil {
		s.logger.Error("create diagnostic request", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to create diagnostic request")
		return
	}

	msg, err := ws.Encode(ws.TypeDiagRequest, requestID, ws.DiagRequestData{Scope: string(body.Scope)})
	if err != nil {
		s.logger.Error("encode diag request", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to encode request")
		return
	}

	if err := s.hub.Send(ctx, deviceID, msg); err != nil {
		s.logger.Error("send diag request", "device_id", deviceID, "err", err)
		writeError(w, http.StatusServiceUnavailable, "failed to deliver request to device")
		return
	}

	writeJSON(w, http.StatusAccepted, result)
}

func (s *Server) handleListDiagnostics(w http.ResponseWriter, r *http.Request) {
	deviceID := r.PathValue("id")

	results, err := s.store.ListDiagnosticResults(r.Context(), deviceID, 50)
	if err != nil {
		s.logger.Error("list diagnostics", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to list diagnostics")
		return
	}

	writeJSON(w, http.StatusOK, results)
}
