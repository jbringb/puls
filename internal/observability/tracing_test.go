package observability

import (
	"context"
	"testing"
)

func TestSetupTracingNoOp(t *testing.T) {
	shutdown, err := SetupTracing(context.Background(), "")
	if err != nil {
		t.Fatalf("SetupTracing with empty endpoint: %v", err)
	}
	if shutdown == nil {
		t.Fatal("expected non-nil shutdown func")
	}
	if err := shutdown(context.Background()); err != nil {
		t.Fatalf("no-op shutdown returned error: %v", err)
	}
}
