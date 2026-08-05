package app_test

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/bArtyom/n2sql-agent/internal/app"
)

type lifecycleServerStub struct {
	listenErr     error
	shutdownErr   error
	shutdown      chan struct{}
	shutdownCalls int
}

func (s *lifecycleServerStub) ListenAndServe() error {
	if s.listenErr != nil {
		return s.listenErr
	}
	<-s.shutdown
	return http.ErrServerClosed
}

func (s *lifecycleServerStub) Shutdown(context.Context) error {
	s.shutdownCalls++
	if s.shutdown != nil {
		close(s.shutdown)
	}
	return s.shutdownErr
}

func TestRunServerShutsDownWhenContextIsCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	server := &lifecycleServerStub{shutdown: make(chan struct{})}

	result := make(chan error, 1)
	go func() { result <- app.RunServer(ctx, server, time.Second) }()
	cancel()

	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("RunServer() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("RunServer() did not return after cancellation")
	}
	if server.shutdownCalls != 1 {
		t.Fatalf("shutdown calls = %d, want 1", server.shutdownCalls)
	}
}

func TestRunServerReturnsListenFailure(t *testing.T) {
	want := errors.New("listen failed")
	server := &lifecycleServerStub{listenErr: want}

	err := app.RunServer(context.Background(), server, time.Second)
	if err == nil || !errors.Is(err, want) {
		t.Fatalf("RunServer() error = %v, want %v", err, want)
	}
}
