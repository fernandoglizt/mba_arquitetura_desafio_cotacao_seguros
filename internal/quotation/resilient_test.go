package quotation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/GuilhermeOliveira591/mba_arquitetura_desafio_cotacao_seguros/internal/partner"
	"github.com/GuilhermeOliveira591/mba_arquitetura_desafio_cotacao_seguros/internal/platform"
)

type memoryCache struct {
	mu      sync.Mutex
	entries map[string]CachedQuote
}

func (c *memoryCache) Get(_ context.Context, key string) (CachedQuote, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, found := c.entries[key]
	return entry, found, nil
}

func (c *memoryCache) Put(_ context.Context, key string, entry CachedQuote, _ time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[key] = entry
	return nil
}

type quoteFunc func(context.Context, platform.Partner, any) (partner.Quote, error)

func (f quoteFunc) Quote(ctx context.Context, p platform.Partner, data any) (partner.Quote, error) {
	return f(ctx, p, data)
}

func TestCacheSeparatesBrokersAndDoesNotServeExpiredQuotes(t *testing.T) {
	now := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	cache := &memoryCache{entries: map[string]CachedQuote{}}
	calls := 0
	quoter := quoteFunc(func(_ context.Context, p platform.Partner, _ any) (partner.Quote, error) {
		calls++
		return partner.Quote{
			Partner: p.Name, QuoteID: fmt.Sprintf("quote-%d", calls),
			PremiumCents: 100, ValidForSeconds: 60,
		}, nil
	})
	s, err := NewResilientService(threePartners[:1], quoter, cache, 4*time.Minute, "01234567890123456789012345678901")
	if err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return now }

	request := validRequest()
	first, err := s.Quote(context.Background(), "corretora-a", request)
	if err != nil || first.Quotes[0].QuoteID != "quote-1" {
		t.Fatalf("first request: %v, %+v", err, first)
	}
	now = now.Add(15 * time.Second)
	second, err := s.Quote(context.Background(), "corretora-a", request)
	if err != nil || second.Quotes[0].QuoteID != "quote-1" || second.Quotes[0].ValidForSeconds != 45 ||
		second.Sources[0].Source != "cache" || *second.Sources[0].AgeSeconds != 15 {
		t.Fatalf("expected cached quote with remaining validity: %v, %+v", err, second)
	}

	other, err := s.Quote(context.Background(), "corretora-b", request)
	if err != nil || other.Quotes[0].QuoteID != "quote-2" {
		t.Fatalf("broker isolation failed: %v, %+v", err, other)
	}
	now = now.Add(46 * time.Second)
	refreshed, err := s.Quote(context.Background(), "corretora-a", request)
	if err != nil || refreshed.Quotes[0].QuoteID != "quote-3" || refreshed.Sources[0].Source != "live" {
		t.Fatalf("expired quote reused: %v, %+v", err, refreshed)
	}
}

func TestResilientAPIShowsPartialAndUnavailableQuotes(t *testing.T) {
	for _, test := range []struct {
		name   string
		fail   bool
		status int
		quotes int
	}{
		{"partial", false, http.StatusOK, 2},
		{"all unavailable", true, http.StatusServiceUnavailable, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			quoter := quoteFunc(func(_ context.Context, p platform.Partner, _ any) (partner.Quote, error) {
				if test.fail || p.Name == "partner-flaky" {
					return partner.Quote{}, errors.New("unavailable")
				}
				return partner.Quote{Partner: p.Name, QuoteID: p.Name + "-quote", ValidForSeconds: 300}, nil
			})
			service, err := NewResilientService(threePartners, quoter, &memoryCache{
				entries: map[string]CachedQuote{},
			}, 4*time.Minute, "")
			if err != nil {
				t.Fatal(err)
			}
			api := NewAPI(service, []string{"corretora-a"})
			res := postQuotes(api.Routes(), "corretora-a", validBody)
			var result Response
			if err := json.Unmarshal(res.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if res.Code != test.status || len(result.Quotes) != test.quotes || !result.Degraded ||
				len(result.MissingPartners) != len(threePartners)-test.quotes {
				t.Fatalf("unexpected response %d: %s", res.Code, res.Body)
			}
			if test.fail && (result.Error != "no_quotes_available" || result.NextAction != "retry") {
				t.Fatalf("missing fallback guidance: %s", res.Body)
			}
		})
	}
}
