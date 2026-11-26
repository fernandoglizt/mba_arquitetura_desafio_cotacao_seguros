package partner

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GuilhermeOliveira591/mba_arquitetura_desafio_cotacao_seguros/internal/platform"
)

func TestProtectedClientOpensSkipsAndRecovers(t *testing.T) {
	var calls atomic.Int32
	p, closeServer := testPartner(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) <= 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{"quote_id":"quote-after-recovery","valid_for_seconds":300}`))
	}))
	defer closeServer()

	now := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	client := NewProtectedClient([]platform.Partner{p}, platform.Resilience{
		PartnerTimeout: 2 * time.Second,
		FailureLimit:   3, FailureWindow: 12 * time.Second, OpenTime: 3 * time.Second,
	})
	client.now = func() time.Time { return now }
	for range 3 {
		if _, err := client.Quote(context.Background(), p, nil); err == nil {
			t.Fatal("expected upstream failure")
		}
	}
	if _, err := client.Quote(context.Background(), p, nil); !errors.Is(err, ErrCircuitUnavailable) {
		t.Fatalf("open circuit: %v", err)
	}
	if calls.Load() != 3 {
		t.Fatalf("request escaped the circuit: %d calls", calls.Load())
	}

	now = now.Add(3 * time.Second)
	if _, err := client.Quote(context.Background(), p, nil); err != nil {
		t.Fatalf("recovery probe: %v", err)
	}
	if state := client.circuits[p.Name].state; state != closed {
		t.Fatalf("circuit after recovery: %s", state)
	}
	if _, err := client.Quote(context.Background(), p, nil); err != nil || calls.Load() != 5 {
		t.Fatalf("closed circuit did not resume: %v, calls %d", err, calls.Load())
	}
}

func TestHalfOpenAdmitsOneProbeAndIgnoresOldCompletions(t *testing.T) {
	b := newBreaker(1, 12*time.Second, 3*time.Second)
	now := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	var destinations []state
	observe := func(_ state, to state) { destinations = append(destinations, to) }

	first, _ := b.admit(now, observe)
	late, _ := b.admit(now, observe)
	b.complete(first, failed, now, observe)
	b.complete(late, succeeded, now, observe)

	now = now.Add(3 * time.Second)
	probe, allowed := b.admit(now, observe)
	if !allowed || b.state != halfOpen {
		t.Fatal("expected half-open probe")
	}
	if _, allowed := b.admit(now, observe); allowed {
		t.Fatal("admitted a second probe")
	}
	b.complete(probe, failed, now, observe)
	if len(destinations) != 3 || destinations[0] != open || destinations[1] != halfOpen || destinations[2] != open {
		t.Fatalf("unexpected transitions: %v", destinations)
	}
}
