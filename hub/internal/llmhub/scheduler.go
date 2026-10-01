package llmhub

import (
	"context"
	"errors"
	"sync"
	"time"
)

var ErrQueueFull = errors.New("request queue is full")
var ErrTokens = errors.New("request token reservation exceeds pool TPM")
var ErrPool = errors.New("rate pool is unavailable")

type rateEvent struct {
	id     string
	at     time.Time
	tokens int64
}
type waiter struct {
	id     string
	tokens int64
	ready  chan struct{}
	lease  *Lease
	err    error
}
type poolState struct {
	config   Pool
	active   int
	waiting  []*waiter
	events   []*rateEvent
	cooldown time.Time
	enabled  bool
}

type Scheduler struct {
	mu     sync.Mutex
	pools  map[string]*poolState
	store  *Store
	window time.Duration
	stop   chan struct{}
	done   chan struct{}
	once   sync.Once
	closed bool
}

type Lease struct {
	scheduler *Scheduler
	pool      *poolState
	event     *rateEvent
	once      sync.Once
}

type PoolStats struct {
	Pool
	Active           int    `json:"active"`
	Queued           int    `json:"queued"`
	RequestsInWindow int    `json:"requests_in_window"`
	TokensInWindow   int64  `json:"tokens_in_window"`
	CooldownUntil    string `json:"cooldown_until,omitempty"`
}

func NewScheduler(store *Store, pools []Pool) (*Scheduler, error) {
	events, err := store.RateEvents(time.Now().Add(-time.Minute))
	if err != nil {
		return nil, err
	}
	s := &Scheduler{pools: map[string]*poolState{}, store: store, window: time.Minute, stop: make(chan struct{}), done: make(chan struct{})}
	for id, ev := range events {
		s.pools[id] = &poolState{events: ev}
	}
	s.Update(pools)
	go s.run()
	return s, nil
}

func (s *Scheduler) run() {
	defer close(s.done)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-s.stop:
			return
		case now := <-ticker.C:
			s.mu.Lock()
			for _, p := range s.pools {
				s.dispatch(p, now)
			}
			s.mu.Unlock()
		}
	}
}

func (s *Scheduler) Close() {
	s.once.Do(func() {
		s.mu.Lock()
		s.closed = true
		for _, p := range s.pools {
			for _, w := range p.waiting {
				w.err = ErrPool
				close(w.ready)
			}
			p.waiting = nil
		}
		s.mu.Unlock()
		close(s.stop)
		<-s.done
	})
}

func (s *Scheduler) Update(pools []Pool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.pools {
		p.enabled = false
	}
	for _, cfg := range pools {
		p := s.pools[cfg.ID]
		if p == nil {
			p = &poolState{}
			s.pools[cfg.ID] = p
		}
		p.config = cfg
		p.enabled = true
	}
	for _, p := range s.pools {
		if !p.enabled {
			for _, w := range p.waiting {
				w.err = ErrPool
				close(w.ready)
			}
			p.waiting = nil
		} else {
			s.dispatch(p, time.Now())
		}
	}
}

func (s *Scheduler) Acquire(ctx context.Context, pool string, id string, tokens int64) (*Lease, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	p := s.pools[pool]
	if s.closed || p == nil || !p.enabled {
		s.mu.Unlock()
		return nil, ErrPool
	}
	if p.config.TPM > 0 && tokens > p.config.TPM {
		s.mu.Unlock()
		return nil, ErrTokens
	}
	s.dispatch(p, time.Now())
	w := &waiter{id: id, tokens: tokens, ready: make(chan struct{})}
	// An empty waiting buffer still allows an immediately dispatchable request.
	if len(p.waiting) >= p.config.QueueSize && (len(p.waiting) > 0 || !s.canDispatch(p, tokens, time.Now())) {
		s.mu.Unlock()
		return nil, ErrQueueFull
	}
	p.waiting = append(p.waiting, w)
	s.dispatch(p, time.Now())
	s.mu.Unlock()
	select {
	case <-w.ready:
		if ctx.Err() != nil && w.lease != nil {
			w.lease.Finish(tokens)
			return nil, ctx.Err()
		}
		return w.lease, w.err
	case <-ctx.Done():
		s.mu.Lock()
		for i, x := range p.waiting {
			if x == w {
				p.waiting = append(p.waiting[:i], p.waiting[i+1:]...)
				break
			}
		}
		lease := w.lease
		s.dispatch(p, time.Now())
		s.mu.Unlock()
		if lease != nil {
			lease.Finish(tokens)
		}
		return nil, ctx.Err()
	}
}

func (s *Scheduler) prune(p *poolState, now time.Time) {
	n := 0
	for n < len(p.events) && !p.events[n].at.After(now.Add(-s.window)) {
		n++
	}
	if n > 0 {
		copy(p.events, p.events[n:])
		for i := len(p.events) - n; i < len(p.events); i++ {
			p.events[i] = nil
		}
		p.events = p.events[:len(p.events)-n]
	}
}

func (s *Scheduler) canDispatch(p *poolState, tokens int64, now time.Time) bool {
	s.prune(p, now)
	if !p.enabled || p.active >= p.config.Concurrency || now.Before(p.cooldown) || (p.config.RPM > 0 && len(p.events) >= p.config.RPM) {
		return false
	}
	var used int64
	for _, e := range p.events {
		used += e.tokens
	}
	return p.config.TPM == 0 || used+tokens <= p.config.TPM
}

func (s *Scheduler) dispatch(p *poolState, now time.Time) {
	s.prune(p, now)
	for len(p.waiting) > 0 {
		w := p.waiting[0]
		if p.config.TPM > 0 && w.tokens > p.config.TPM {
			p.waiting = p.waiting[1:]
			w.err = ErrTokens
			close(w.ready)
			continue
		}
		if !s.canDispatch(p, w.tokens, now) {
			return
		}
		p.waiting = p.waiting[1:]
		if err := s.store.Dispatch(w.id, p.config.ID, now, w.tokens); err != nil {
			w.err = err
			close(w.ready)
			continue
		}
		e := &rateEvent{id: w.id, at: now, tokens: w.tokens}
		p.events = append(p.events, e)
		p.active++
		w.lease = &Lease{scheduler: s, pool: p, event: e}
		close(w.ready)
	}
}

// Finish reconciles the conservative reservation with reported usage and releases concurrency.
func (l *Lease) Finish(tokens int64) {
	l.once.Do(func() {
		s := l.scheduler
		s.mu.Lock()
		defer s.mu.Unlock()
		l.pool.active--
		l.event.tokens = tokens
		// A failed persistence update leaves the larger original reservation on disk.
		_ = s.store.UpdateRate(l.event.id, tokens)
		s.dispatch(l.pool, time.Now())
	})
}

func (s *Scheduler) Cooldown(pool string, until time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p := s.pools[pool]; p != nil && until.After(p.cooldown) {
		p.cooldown = until
	}
}

func (s *Scheduler) Stats() []PoolStats {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []PoolStats{}
	for _, p := range s.pools {
		if !p.enabled {
			continue
		}
		s.prune(p, time.Now())
		stat := PoolStats{Pool: p.config, Active: p.active, Queued: len(p.waiting), RequestsInWindow: len(p.events)}
		for _, e := range p.events {
			stat.TokensInWindow += e.tokens
		}
		if p.cooldown.After(time.Now()) {
			stat.CooldownUntil = p.cooldown.UTC().Format(time.RFC3339)
		}
		out = append(out, stat)
	}
	return out
}
