package quotation

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"sort"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/GuilhermeOliveira591/mba_arquitetura_desafio_cotacao_seguros/internal/partner"
	"github.com/GuilhermeOliveira591/mba_arquitetura_desafio_cotacao_seguros/internal/platform"
)

var ErrNoQuotes = errors.New("no quotes available")

type CachedQuote struct {
	Quote     partner.Quote
	QuotedAt  time.Time
	ExpiresAt time.Time
}

type QuoteCache interface {
	Get(context.Context, string) (CachedQuote, bool, error)
	Put(context.Context, string, CachedQuote, time.Duration) error
}

type ResilientService struct {
	partners []platform.Partner
	quoter   Quoter
	cache    QuoteCache
	ttl      time.Duration
	key      []byte
	now      func() time.Time
	lookups  metric.Int64Counter
}

func NewResilientService(partners []platform.Partner, quoter Quoter, cache QuoteCache, ttl time.Duration, secret string) (*ResilientService, error) {
	key := []byte(secret)
	if len(key) == 0 {
		key = make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return nil, err
		}
	}

	lookups, err := otel.Meter("quotation-api").Int64Counter("quote_cache_lookups")
	if err != nil {
		return nil, err
	}
	return &ResilientService{
		partners: partners, quoter: quoter, cache: cache, ttl: ttl,
		key: key, now: time.Now, lookups: lookups,
	}, nil
}

type partnerResult struct {
	quote  partner.Quote
	source QuoteSource
	err    error
}

func (s *ResilientService) Quote(ctx context.Context, tenant string, request Request) (Response, error) {
	start := s.now()
	fingerprint, err := s.fingerprint(request)
	if err != nil {
		return Response{}, err
	}

	results := make([]partnerResult, len(s.partners))
	var calls sync.WaitGroup
	for i, p := range s.partners {
		calls.Go(func() {
			results[i] = s.quotePartner(ctx, p, partnerRequest{Broker: tenant, Request: request}, fingerprint)
		})
	}
	calls.Wait()

	response := Response{TenantID: tenant, Quotes: make([]partner.Quote, 0, len(results))}
	var available []partnerResult
	for i, result := range results {
		if result.err != nil {
			response.MissingPartners = append(response.MissingPartners, s.partners[i].Name)
			continue
		}
		available = append(available, result)
	}
	sort.SliceStable(available, func(i, j int) bool {
		return available[i].quote.PremiumCents < available[j].quote.PremiumCents
	})
	for _, result := range available {
		response.Quotes = append(response.Quotes, result.quote)
		response.Sources = append(response.Sources, result.source)
	}
	response.Degraded = len(response.MissingPartners) > 0
	response.ElapsedMs = s.now().Sub(start).Milliseconds()
	if len(response.Quotes) == 0 {
		response.Degraded = true
		response.Error = "no_quotes_available"
		response.NextAction = "retry"
		return response, ErrNoQuotes
	}
	return response, nil
}

func (s *ResilientService) fingerprint(request Request) (string, error) {
	body, err := json.Marshal(request)
	if err != nil {
		return "", err
	}
	hash := hmac.New(sha256.New, s.key)
	hash.Write(body)
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func cacheKey(tenant, partner, fingerprint string) string {
	return "quotation:v1:" + url.QueryEscape(tenant) + ":" + url.QueryEscape(partner) + ":" + fingerprint
}

func (s *ResilientService) quotePartner(ctx context.Context, p platform.Partner, req partnerRequest, fingerprint string) partnerResult {
	key := cacheKey(req.Broker, p.Name, fingerprint)
	entry, found, cacheErr := s.cache.Get(ctx, key)
	now := s.now()
	span := trace.SpanFromContext(ctx)
	partnerAttr := attribute.String("partner", p.Name)

	remaining := entry.ExpiresAt.Sub(now)
	if cacheErr == nil && found && entry.Quote.Partner == p.Name && remaining >= time.Second {
		s.lookups.Add(ctx, 1, metric.WithAttributes(partnerAttr, attribute.String("result", "hit")))
		span.AddEvent("quote.cache.hit", trace.WithAttributes(partnerAttr))
		age := int64(max(0, now.Sub(entry.QuotedAt)) / time.Second)
		entry.Quote.ValidForSeconds = int64(remaining / time.Second)
		return partnerResult{
			quote:  entry.Quote,
			source: QuoteSource{Partner: p.Name, Source: "cache", QuotedAt: entry.QuotedAt, AgeSeconds: &age},
		}
	}
	s.lookups.Add(ctx, 1, metric.WithAttributes(partnerAttr, attribute.String("result", "miss")))
	if cacheErr != nil {
		span.AddEvent("quote.cache.read_error", trace.WithAttributes(partnerAttr))
	}
	quote, err := s.quoter.Quote(ctx, p, req)
	if err != nil {
		return partnerResult{err: err}
	}
	quotedAt := s.now().UTC()
	if seconds := min(quote.ValidForSeconds, int64(s.ttl/time.Second)); seconds > 0 {
		ttl := time.Duration(seconds) * time.Second
		entry := CachedQuote{Quote: quote, QuotedAt: quotedAt, ExpiresAt: quotedAt.Add(ttl)}
		if err := s.cache.Put(ctx, key, entry, ttl); err != nil {
			span.AddEvent("quote.cache.write_error", trace.WithAttributes(partnerAttr))
		}
	}
	return partnerResult{quote: quote, source: QuoteSource{Partner: p.Name, Source: "live", QuotedAt: quotedAt}}
}
