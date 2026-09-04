package main

import (
	"crypto/sha256"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRequireBearerRoutesCredentialRole(t *testing.T) {
	writerToken := "01234567890123456789012345678901"
	readOnlyToken := "11234567890123456789012345678901"
	writerHash := sha256.Sum256([]byte(writerToken))
	readOnlyHash := sha256.Sum256([]byte(readOnlyToken))
	handler := requireBearer(
		writerHash,
		readOnlyHash,
		http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
			response.Header().Set("X-Credential-Role", "writer")
			response.WriteHeader(http.StatusNoContent)
		}),
		http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
			response.Header().Set("X-Credential-Role", "read-only")
			response.WriteHeader(http.StatusNoContent)
		}),
	)
	for _, test := range []struct {
		name     string
		header   string
		wantCode int
		wantRole string
	}{
		{name: "missing", wantCode: http.StatusUnauthorized},
		{name: "wrong", header: "Bearer wrong", wantCode: http.StatusUnauthorized},
		{name: "malformed", header: writerToken, wantCode: http.StatusUnauthorized},
		{name: "writer", header: "Bearer " + writerToken, wantCode: http.StatusNoContent, wantRole: "writer"},
		{name: "read-only", header: "Bearer " + readOnlyToken, wantCode: http.StatusNoContent, wantRole: "read-only"},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/mcp", nil)
			request.Header.Set("Authorization", test.header)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.wantCode {
				t.Fatalf("status = %d, want %d", response.Code, test.wantCode)
			}
			if got := response.Header().Get("X-Credential-Role"); got != test.wantRole {
				t.Fatalf("role = %q, want %q", got, test.wantRole)
			}
		})
	}
}

func TestHealth(t *testing.T) {
	response := httptest.NewRecorder()
	health(response, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if response.Code != http.StatusOK || response.Body.String() != "{\"ok\":true,\"service\":\"hl-mcp\"}\n" {
		t.Fatalf("health response = %d %q", response.Code, response.Body.String())
	}
}
