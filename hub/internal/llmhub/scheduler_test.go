package llmhub

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestSchedulerFIFOConcurrencyCancellationAndCapacity(t *testing.T) {
	s, err := NewScheduler(testStore(t), []Pool{{ID: "shared", Concurrency: 1, QueueSize: 1}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	first, err := s.Acquire(context.Background(), "shared", "first", 10)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		l, err := s.Acquire(ctx, "shared", "second", 10)
		if l != nil {
			l.Finish(10)
		}
		result <- err
	}()
	waitQueued(t, s, 1)
	if _, err := s.Acquire(context.Background(), "shared", "third", 10); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("queue capacity: %v", err)
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancel did not release waiter")
	}
	first.Finish(5)
	first.Finish(5)
	if stat := s.Stats()[0]; stat.Active != 0 || stat.Queued != 0 {
		t.Fatalf("leak: %+v", stat)
	}
}

func waitQueued(t *testing.T, s *Scheduler, count int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if s.Stats()[0].Queued == count {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("waiter was not queued")
}

func TestSchedulerSharedRPMTPMCooldownAndRestart(t *testing.T) {
	store := testStore(t)
	pools := []Pool{{ID: "shared", Concurrency: 2, QueueSize: 4, RPM: 2, TPM: 100}}
	s, err := NewScheduler(store, pools)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Acquire(context.Background(), "shared", "oversized", 101); !errors.Is(err, ErrTokens) {
		t.Fatal("oversized request accepted")
	}
	l, _ := s.Acquire(context.Background(), "shared", "one", 80)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	if _, err := s.Acquire(ctx, "shared", "two", 30); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("TPM oversubscribed")
	}
	l.Finish(10)
	l, err = s.Acquire(context.Background(), "shared", "three", 20)
	if err != nil {
		t.Fatal(err)
	}
	l.Finish(20)
	s.Close()
	s, err = NewScheduler(store, pools)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx, cancel = context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	if _, err := s.Acquire(ctx, "shared", "four", 1); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("restart reset RPM history")
	}
	s.Update([]Pool{{ID: "shared", Concurrency: 2, QueueSize: 4}})
	s.Cooldown("shared", time.Now().Add(time.Second))
	ctx, cancel = context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	if _, err := s.Acquire(ctx, "shared", "cooldown", 1); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("cooldown bypassed")
	}
}

func TestSchedulerConcurrentDispatchAndShutdown(t *testing.T) {
	s, err := NewScheduler(testStore(t), []Pool{{ID: "shared", Concurrency: 3, QueueSize: 50}})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			l, err := s.Acquire(ctx, "shared", randomID("job_"), 10)
			if err != nil {
				t.Error(err)
				return
			}
			if stat := s.Stats()[0]; stat.Active > 3 {
				t.Error("concurrency limit exceeded")
			}
			l.Finish(5)
		}()
	}
	wg.Wait()
	s.Close()
	if _, err := s.Acquire(context.Background(), "shared", "closed", 1); !errors.Is(err, ErrPool) {
		t.Fatal("closed scheduler accepted traffic")
	}
}
