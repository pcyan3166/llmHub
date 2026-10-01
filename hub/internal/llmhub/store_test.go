package llmhub

import (
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestBudgetReservationIsAtomicAndSettlementIdempotent(t *testing.T) {
	s := testStore(t)
	var accepted atomic.Int32
	var winner string
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id := randomID("attempt_")
			err := s.Reserve(Attempt{ID: id, ProjectID: "p", InputTokens: 10, OutputTokens: 5}, 0.0001, 100)
			if err == nil {
				accepted.Add(1)
				mu.Lock()
				winner = id
				mu.Unlock()
			} else if !errors.Is(err, ErrBudget) {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if accepted.Load() != 1 {
		t.Fatalf("budget oversubscribed: %d", accepted.Load())
	}
	for i := 0; i < 2; i++ {
		if err := s.Settle(winner, "success", 200, 10, 5, 20, 10, false); err != nil {
			t.Fatal(err)
		}
	}
	u, err := s.Usage("p", time.Now().UTC().Format("2006-01"))
	if err != nil || len(u) != 1 || u[0].CostUSD != 0.00002 || u[0].ReservedUSD != 0 || u[0].InputTokens != 10 || u[0].Requests != 1 {
		t.Fatalf("double settlement: %+v %v", u, err)
	}
}

func TestStoreCrashRecoveryAndSingleInstance(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hub.db")
	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if other, err := OpenStore(path); err == nil {
		other.Close()
		t.Fatal("duplicate process acquired database")
	}
	if err := s.Reserve(Attempt{ID: "interrupted", ProjectID: "p", InputTokens: 10, OutputTokens: 5}, 1, 100); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	rows, _ := s.Recent("p", 10)
	u, _ := s.Usage("p", time.Now().UTC().Format("2006-01"))
	if rows[0].Status != "interrupted" || u[0].ReservedUSD != 0 || u[0].CostUSD != 0.0001 || u[0].Errors != 1 || u[0].InputTokens != 10 || u[0].OutputTokens != 5 {
		t.Fatalf("recovery lost accounting: %+v %+v", rows, u)
	}
}

func TestPriceSnapshotAndCacheBreakdownSurviveRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hub.db")
	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	p := tariffProfile().priceAt(instant("2026-09-28T02:00:00Z"))
	if err := s.Reserve(Attempt{ID: "priced", ProjectID: "p", ProfileID: p.ID, PriceSnapshot: &p}, 1, 1000); err != nil {
		t.Fatal(err)
	}
	p.AppliedPrice.CachedInputTokens, p.AppliedPrice.CacheUsageKnown = 50, true
	if err := s.Settle("priced", "success", 200, 100, 20, 190, 1, false, &p); err != nil {
		t.Fatal(err)
	}
	// A repeated settlement must not replace the original tariff or cached count.
	different := tariffProfile().priceAt(instant("2026-09-28T04:00:00Z"))
	if err := s.Settle("priced", "success", 200, 100, 20, 140, 1, false, &different); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	rows, err := s.Recent("p", 1)
	if err != nil || len(rows) != 1 || rows[0].CostUSD != 0.00019 || rows[0].PriceSnapshot.AppliedPrice.WindowID != "morning" || rows[0].PriceSnapshot.AppliedPrice.CachedInputTokens != 50 {
		t.Fatalf("historical tariff lost: %+v %v", rows, err)
	}
}

func TestRetentionDoesNotResetBudget(t *testing.T) {
	s := testStore(t)
	if err := s.Reserve(Attempt{ID: "old", ProjectID: "p"}, 1, 100); err != nil {
		t.Fatal(err)
	}
	if err := s.Settle("old", "success", 200, 10, 5, 100, 10, false); err != nil {
		t.Fatal(err)
	}
	s.db.Exec("UPDATE attempts SET created_at=?", time.Now().UTC().AddDate(0, 0, -40).Format(time.RFC3339Nano))
	if err := s.PruneRates(); err != nil {
		t.Fatal(err)
	}
	if rows, _ := s.Recent("p", 10); len(rows) != 0 {
		t.Fatal("retention failed")
	}
	if err := s.Reserve(Attempt{ID: "new", ProjectID: "p"}, 0.0001, 1); !errors.Is(err, ErrBudget) {
		t.Fatal("pruning reset budget")
	}
}
