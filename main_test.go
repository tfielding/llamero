package main

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// sleepyOllama is unreachable until it has been woken, then lists one model.
type sleepyOllama struct {
	mu    sync.Mutex
	awake bool
}

func (s *sleepyOllama) Models(context.Context) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.awake {
		return nil, errors.New("connection refused")
	}
	return []string{"gpt-oss:20b"}, nil
}

func TestFirstModelWakesOllama(t *testing.T) {
	s := &sleepyOllama{}
	wakes := 0
	wake := func(context.Context) error {
		s.mu.Lock()
		defer s.mu.Unlock()
		wakes++
		s.awake = true
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	model, err := firstModel(ctx, s, wake, 5*time.Millisecond)
	if err != nil || model != "gpt-oss:20b" {
		t.Fatalf("firstModel = %q, %v", model, err)
	}
	if wakes != 1 {
		t.Fatalf("wake sent %d times, want 1", wakes)
	}
}

func TestFirstModelWithoutWake(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := firstModel(ctx, &sleepyOllama{}, nil, 5*time.Millisecond); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want deadline exceeded", err)
	}
}

func TestLocalAddr(t *testing.T) {
	for in, want := range map[string]string{":8092": "127.0.0.1:8092", "0.0.0.0:8092": "127.0.0.1:8092",
		"[::]:8092": "[::1]:8092", "10.0.0.5:8092": "10.0.0.5:8092"} {
		if got := localAddr(in); got != want {
			t.Errorf("localAddr(%q) = %q, want %q", in, got, want)
		}
	}
}
