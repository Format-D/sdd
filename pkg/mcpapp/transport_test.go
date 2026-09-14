package mcpapp_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestRunEndsOnDisconnectOrCancellation(t *testing.T) {
	for _, tt := range []struct {
		name   string
		cancel bool
	}{
		{name: "client disconnects"},
		{name: "caller cancels", cancel: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			env := newTestServer(t, nil, "", "")
			serverTransport, clientTransport := mcp.NewInMemoryTransports()
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() {
				done <- env.srv.Run(ctx, serverTransport)
			}()
			client := connect(t, env.srv, clientTransport)
			if tt.cancel {
				cancel()
			} else if err := client.Close(); err != nil {
				t.Fatal(err)
			}
			var err error
			select {
			case err = <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("Run did not stop after disconnect or cancellation")
			}
			if tt.cancel && !errors.Is(err, context.Canceled) {
				t.Fatalf("Run cancellation = %v, want context.Canceled", err)
			}
			if !tt.cancel && err != nil {
				t.Fatalf("Run after disconnect = %v", err)
			}
		})
	}
}
