package api

import (
	"encoding/json"
	"net/http"
)

// envelope is the shape of every JSON response. Data and Error are omitted
// when nil, so exactly one of them is present:
//
//	{"success": true,  "data": ...}
//	{"success": false, "error": {"code": "not_found", "message": "..."}}
type envelope struct {
	Success bool      `json:"success"`
	Data    any       `json:"data,omitempty"`
	Error   *apiError `json:"error,omitempty"`
}

// apiError.Code is stable and meant for programs; Message is for humans.
type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func writeData(w http.ResponseWriter, status int, data any) {
	writeJSON(w, status, envelope{Success: true, Data: data})
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, envelope{Error: &apiError{Code: errorCode(status), Message: message}})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v) // an error here means the client went away
}

// errorCode maps an HTTP status to the stable error code clients branch on.
func errorCode(status int) string {
	switch status {
	case http.StatusBadRequest:
		return "invalid_request"
	case http.StatusNotFound:
		return "not_found"
	case http.StatusConflict:
		return "conflict"
	case http.StatusRequestEntityTooLarge:
		return "payload_too_large"
	case http.StatusServiceUnavailable:
		return "unavailable"
	default:
		return "internal"
	}
}
