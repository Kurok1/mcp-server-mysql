/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since 1.2.1
 */
package transport

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const MCPPath = "/mcp"

var basicHostOrigins = map[string]struct{}{
	"http://localhost:8080": {},
	"http://127.0.0.1:8080": {},
	"http://[::1]:8080":     {},
}

// ValidateLoopbackAddress rejects wildcard and non-loopback HTTP listeners.
func ValidateLoopbackAddress(address string) error {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("invalid listen address %q: %w", address, err)
	}
	if port == "" {
		return fmt.Errorf("listen address %q is missing a port", address)
	}
	if strings.EqualFold(host, "localhost") {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("listen address %q must use localhost or a loopback IP", address)
	}
	return nil
}

func withBasicHostCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" {
			if _, allowed := basicHostOrigins[origin]; !allowed {
				http.Error(w, "origin is not allowed", http.StatusForbidden)
				return
			}
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Accept, Content-Type, Last-Event-ID, MCP-Protocol-Version, MCP-Session-Id")
			w.Header().Set("Access-Control-Expose-Headers", "MCP-Session-Id")
			w.Header().Add("Vary", "Origin")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// NewHTTPHandler exposes a stateless Streamable HTTP endpoint at /mcp.
func NewHTTPHandler(server *mcp.Server) http.Handler {
	streamable := mcp.NewStreamableHTTPHandler(
		func(r *http.Request) *mcp.Server { return server },
		&mcp.StreamableHTTPOptions{
			Stateless:                    true,
			JSONResponse:                 true,
			PropagateRequestCancellation: true,
		},
	)
	mux := http.NewServeMux()
	mux.Handle(MCPPath, streamable)
	return withBasicHostCORS(mux)
}

// RunHTTP serves the local development transport until the context is cancelled.
func RunHTTP(ctx context.Context, address string, server *mcp.Server) error {
	if err := ValidateLoopbackAddress(address); err != nil {
		return err
	}
	httpServer := &http.Server{
		Addr:              address,
		Handler:           NewHTTPHandler(server),
		ReadHeaderTimeout: 5 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() { errCh <- httpServer.ListenAndServe() }()

	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("shutdown HTTP server: %w", err)
		}
		return nil
	}
}
