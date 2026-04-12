package adapter

import (
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"
)

func TestBaseAdapter_OnStateChange_CalledOnTransition(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	a := NewBaseAdapter("test", "ws://invalid", 30*time.Second, 0, logger)

	var mu sync.Mutex
	var transitions []string

	a.SetOnStateChange(func(name, oldState, newState string) {
		mu.Lock()
		transitions = append(transitions, oldState+"->"+newState)
		mu.Unlock()
	})

	// Simulate state changes
	a.setConnState("connecting")
	a.setConnState("connected")
	a.setConnState("disconnected")

	mu.Lock()
	defer mu.Unlock()

	if len(transitions) != 3 {
		t.Fatalf("expected 3 transitions, got %d: %v", len(transitions), transitions)
	}
	if transitions[0] != "disconnected->connecting" {
		t.Errorf("expected disconnected->connecting, got %s", transitions[0])
	}
	if transitions[1] != "connecting->connected" {
		t.Errorf("expected connecting->connected, got %s", transitions[1])
	}
	if transitions[2] != "connected->disconnected" {
		t.Errorf("expected connected->disconnected, got %s", transitions[2])
	}
}

func TestBaseAdapter_OnStateChange_NotCalledForSameState(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	a := NewBaseAdapter("test", "ws://invalid", 30*time.Second, 0, logger)

	callCount := 0
	a.SetOnStateChange(func(name, oldState, newState string) {
		callCount++
	})

	a.setConnState("disconnected") // same as initial state
	if callCount != 0 {
		t.Errorf("expected 0 calls for same state, got %d", callCount)
	}
}

func TestBaseAdapter_OnStateChange_IncludesName(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	a := NewBaseAdapter("binance", "ws://invalid", 30*time.Second, 0, logger)

	var gotName string
	a.SetOnStateChange(func(name, oldState, newState string) {
		gotName = name
	})

	a.setConnState("connecting")
	if gotName != "binance" {
		t.Errorf("expected name binance, got %s", gotName)
	}
}
