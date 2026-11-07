package main

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tfielding/llamero/internal/transport"
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

func TestListenRunsEveryTransport(t *testing.T) {
	router := transport.NewRouter()
	router.Add("", nil)
	router.Add("telegram", nil)
	var mu sync.Mutex
	var senders []string
	handle := func(_ context.Context, m transport.Message) {
		mu.Lock()
		senders = append(senders, m.Sender)
		mu.Unlock()
	}
	// Each fake transport delivers one message, then waits for shutdown.
	fakeListener := func(name, prefix, sender string) listener {
		return listener{name: name, prefix: prefix, listen: func(ctx context.Context, h func(context.Context, transport.Message)) error {
			h(ctx, transport.Message{Sender: sender, Text: "hi"})
			<-ctx.Done()
			return ctx.Err()
		}}
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error)
	go func() {
		done <- listen(ctx, router, []listener{fakeListener("signal", "", "+15550000001"), fakeListener("telegram", "telegram", "12345")}, handle)
	}()
	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		n := len(senders)
		mu.Unlock()
		if n == 2 || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Errorf("shutdown: %v", err)
	}
	sort.Strings(senders)
	if strings.Join(senders, ",") != "+15550000001,telegram:12345" {
		t.Errorf("senders: %v", senders)
	}

	// A transport that fails stops the others, and its error is returned.
	failing := listener{name: "broken", listen: func(context.Context, func(context.Context, transport.Message)) error {
		return errors.New("bad token")
	}}
	err := listen(context.Background(), router, []listener{failing, fakeListener("signal", "", "+15550000001")}, handle)
	if err == nil || !strings.Contains(err.Error(), "bad token") {
		t.Errorf("failing transport: %v", err)
	}
}
