package cliapp

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLocalHTTPBearerAuth(t *testing.T) {
	for _, tt := range []struct {
		name   string
		header string
		status int
	}{
		{name: "missing token", status: http.StatusUnauthorized},
		{name: "incorrect token", header: "Bearer other", status: http.StatusUnauthorized},
		{name: "correct token", header: "Bearer secret-token", status: http.StatusNoContent},
	} {
		t.Run(tt.name, func(t *testing.T) {
			handler := localBearerAuth("secret-token", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusNoContent)
			}))
			request := httptest.NewRequest(http.MethodGet, "/", nil)
			request.Header.Set("Authorization", tt.header)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != tt.status {
				t.Fatalf("status = %d, want %d", response.Code, tt.status)
			}
		})
	}
}
