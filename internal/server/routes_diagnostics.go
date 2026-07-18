package server

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

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
		s.deleteOrphanedDiagnosticRequest(ctx, requestID)
		writeError(w, http.StatusInternalServerError, "failed to encode request")
		return
	}

	if err := s.hub.Send(ctx, deviceID, msg); err != nil {
		s.logger.Error("send diag request", "device_id", deviceID, "err", err)
		s.deleteOrphanedDiagnosticRequest(ctx, requestID)
		writeError(w, http.StatusServiceUnavailable, "failed to deliver request to device")
		return
	}

	result.Status = model.DiagnosticPending
	writeJSON(w, http.StatusAccepted, result)
}

// deleteOrphanedDiagnosticRequest cleans up a diagnostic_results row after a
// failure known, at request time, to mean the row can never be answered
// (encoding failed, or the device disconnected before delivery). Failure to
// delete is logged, not surfaced — the caller's original error takes
// priority, and an orphaned row still self-resolves via diagnosticStatus's
// timeout once PULS_DIAGNOSTIC_TIMEOUT elapses.
func (s *Server) deleteOrphanedDiagnosticRequest(ctx context.Context, requestID string) {
	if err := s.store.DeleteDiagnosticRequest(ctx, requestID); err != nil {
		s.logger.Warn("delete orphaned diagnostic request", "request_id", requestID, "err", err)
	}
}

func (s *Server) handleListDiagnostics(w http.ResponseWriter, r *http.Request) {
	deviceID := r.PathValue("id")

	results, err := s.store.ListDiagnosticResults(r.Context(), deviceID, 50)
	if err != nil {
		s.logger.Error("list diagnostics", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to list diagnostics")
		return
	}

	for i := range results {
		results[i].Status = diagnosticStatus(results[i], s.cfg.DiagnosticTimeout)
	}
	writeJSON(w, http.StatusOK, results)
}

// diagnosticStatus derives a DiagnosticResult's status: a device can still
// answer after timeout elapses (the row isn't deleted), so TimedOut means
// "stop waiting", not "never will complete".
func diagnosticStatus(d model.DiagnosticResult, timeout time.Duration) model.DiagnosticRequestStatus {
	if d.Payload != nil {
		return model.DiagnosticCompleted
	}
	if time.Since(d.RequestedAt) > timeout {
		return model.DiagnosticTimedOut
	}
	return model.DiagnosticPending
}
