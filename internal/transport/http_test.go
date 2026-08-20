/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since 1.2.1
 */
package transport

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestValidateLoopbackAddress(t *testing.T) {
	for _, address := range []string{"127.0.0.1:3001", "localhost:3001", "[::1]:3001"} {
		if err := ValidateLoopbackAddress(address); err != nil {
			t.Errorf("ValidateLoopbackAddress(%q): %v", address, err)
		}
	}
	for _, address := range []string{":3001", "0.0.0.0:3001", "192.0.2.1:3001", "example.com:3001", "127.0.0.1"} {
		if err := ValidateLoopbackAddress(address); err == nil {
			t.Errorf("ValidateLoopbackAddress(%q) unexpectedly succeeded", address)
		}
	}
}

func TestHTTPHandlerCORSAndPath(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	handler := NewHTTPHandler(server)

	t.Run("allows official Basic Host origin", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodOptions, "http://127.0.0.1:3001/mcp", nil)
		req.Header.Set("Origin", "http://localhost:8080")
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, req)
		if recorder.Code != http.StatusNoContent {
			t.Fatalf("status = %d, want 204", recorder.Code)
		}
		if got := recorder.Header().Get("Access-Control-Allow-Origin"); got != "http://localhost:8080" {
			t.Fatalf("Access-Control-Allow-Origin = %q", got)
		}
		if got := recorder.Header().Get("Access-Control-Allow-Methods"); got != "POST, OPTIONS" {
			t.Fatalf("Access-Control-Allow-Methods = %q", got)
		}
	})

	t.Run("rejects other browser origins", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:3001/mcp", nil)
		req.Header.Set("Origin", "https://example.com")
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, req)
		if recorder.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403", recorder.Code)
		}
	})

	t.Run("does not expose another path", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:3001/other", nil)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, req)
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", recorder.Code)
		}
	})
}
