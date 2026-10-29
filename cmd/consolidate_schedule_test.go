package main

import (
	"errors"
	"testing"
	"time"
)

func TestConsolidationInterval(t *testing.T) {
	cases := []struct {
		env  string
		want time.Duration
	}{
		{"", 0},
		{"24h", 24 * time.Hour},
		{"90m", 90 * time.Minute},
		{"0", 0},
		{"-5m", 0},
		{"not-a-duration", 0},
	}
	for _, tc := range cases {
		t.Setenv("FORGE_CONSOLIDATE_INTERVAL", tc.env)
		if got := consolidationInterval(); got != tc.want {
			t.Errorf("env %q: interval = %v, want %v", tc.env, got, tc.want)
		}
	}
}

func TestConsolidationArgsContract(t *testing.T) {
	want := []string{"consolidate", "--apply"}
	if len(consolidationArgs) != len(want) {
		t.Fatalf("args = %v, want %v", consolidationArgs, want)
	}
	for i := range want {
		if consolidationArgs[i] != want[i] {
			t.Fatalf("args = %v, want %v", consolidationArgs, want)
		}
	}
}

func TestConsolidationLoopRunsAndStops(t *testing.T) {
	original := consolidationCommand
	t.Cleanup(func() { consolidationCommand = original })

	ran := make(chan []string, 4)
	consolidationCommand = func(args []string) ([]byte, error) {
		ran <- args
		return []byte("ok"), nil
	}

	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		consolidationLoop(10*time.Millisecond, stop)
		close(done)
	}()

	select {
	case args := <-ran:
		if len(args) != 2 || args[0] != "consolidate" || args[1] != "--apply" {
			t.Errorf("scheduled args = %v", args)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("scheduled consolidation never ran")
	}

	close(stop)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("consolidation loop did not stop")
	}
}

func TestConsolidationLoopSurvivesFailure(t *testing.T) {
	original := consolidationCommand
	t.Cleanup(func() { consolidationCommand = original })

	attempts := 0
	consolidationCommand = func([]string) ([]byte, error) {
		attempts++
		if attempts == 1 {
			return []byte("boom"), errors.New("exit status 1")
		}
		return nil, nil
	}

	stop := make(chan struct{})
	go consolidationLoop(5*time.Millisecond, stop)
	time.Sleep(80 * time.Millisecond)
	close(stop)

	if attempts < 2 {
		t.Fatalf("attempts = %d, want the loop to retry after a failure", attempts)
	}
}

func TestConsolidationLoopDisabled(t *testing.T) {
	original := consolidationCommand
	t.Cleanup(func() { consolidationCommand = original })

	called := false
	consolidationCommand = func([]string) ([]byte, error) {
		called = true
		return nil, nil
	}

	done := make(chan struct{})
	go func() {
		consolidationLoop(0, make(chan struct{}))
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("disabled loop should return immediately")
	}
	if called {
		t.Error("disabled loop ran the command")
	}
}
