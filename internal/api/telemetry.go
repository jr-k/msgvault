package api

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"io"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
)

const (
	telemetryEventsPath           = "/api/v1/telemetry/events"
	maxTelemetryEventRequestBytes = 64 << 10 // matches kit's capture handler cap, so oversized bodies get this route's 400
)

// TelemetryEventRequest documents the body kit's capture handler accepts.
type TelemetryEventRequest struct {
	Event      string         `json:"event" doc:"Allowlisted event name, such as app_opened"`
	Properties map[string]any `json:"properties,omitempty" doc:"Event properties; the daemon drops any its allowlist omits"`
}

// TelemetryEventResponse documents kit's accepted response.
type TelemetryEventResponse struct {
	Status string `json:"status" enum:"queued,disabled" doc:"queued when the event was sent; disabled when telemetry is off"`
}

func (s *Server) registerTelemetryRoutes(apiV1 huma.API) {
	op := rawAPIV1Operation("captureTelemetryEvent", http.MethodPost, "/telemetry/events", "Report a web UI usage event")
	op.RequestBody = jsonRequestBodyFor[TelemetryEventRequest](apiV1)
	op.Responses = jsonResponsesFor[TelemetryEventResponse](apiV1, http.StatusAccepted)
	// kit writes plain-text errors and the auth layer writes ErrorResponse, so the error body stays undeclared.
	op.Responses["default"] = &huma.Response{Description: "Error"}
	registerRawHumaRoute(apiV1, op, s.handleTelemetryEvent)
}

// handleTelemetryEvent bounds and frames the body, then hands the same bytes to kit, which decodes, admits and captures.
func (s *Server) handleTelemetryEvent(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxTelemetryEventRequestBytes))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "request body too large or unreadable")
		return
	}
	dec := jsontext.NewDecoder(bytes.NewReader(body))
	if err := json.UnmarshalDecode(dec, &struct{}{}); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "request body must be one JSON object")
		return
	}
	if !requireSingleJSONValue(w, dec, "invalid_request") {
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	s.telemetryCapture.ServeHTTP(w, r)
}
