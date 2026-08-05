package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"
)

const defaultShutdownTimeout = 10 * time.Second

type HTTPServer interface {
	ListenAndServe() error
	Shutdown(context.Context) error
}

// RunServer serves HTTP until the listener fails or ctx is canceled. On
// cancellation it stops accepting new requests and waits up to timeout for
// active handlers to finish.
func RunServer(ctx context.Context, server HTTPServer, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = defaultShutdownTimeout
	}
	result := make(chan error, 1)
	go func() { result <- server.ListenAndServe() }()

	select {
	case err := <-result:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serve HTTP: %w", err)
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("shutdown HTTP server: %w", err)
		}
		return nil
	}
}
