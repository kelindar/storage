package main

import (
	"context"
	"testing"
	"time"

	"github.com/kelindar/bench"
)

func TestBenchmarks(t *testing.T) {
	s := newSuite(context.Background())
	defer s.close()

	bench.Run(func(b *bench.B) {
		runBenchmarks(b, s)
	}, bench.WithSamples(2), bench.WithDuration(time.Nanosecond), bench.WithDryRun())
}

func TestAutomation(t *testing.T) {
	got := newAutomation("test automation")
	if got.Name != "test automation" {
		t.Fatalf("Name = %q", got.Name)
	}
	if got.Workflow.Version != 1 {
		t.Fatalf("Workflow.Version = %d", got.Workflow.Version)
	}
}
