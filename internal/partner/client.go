package partner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/GuilhermeOliveira591/mba_arquitetura_desafio_cotacao_seguros/internal/platform"
)

type Quote struct {
	Partner         string `json:"partner"`
	QuoteID         string `json:"quote_id"`
	PremiumCents    int64  `json:"premium_cents"`
	Currency        string `json:"currency"`
	CoverageCents   int64  `json:"coverage_cents"`
	ValidForSeconds int64  `json:"valid_for_seconds"`
}

type Client struct {
	http       *http.Client
	circuits   map[string]*breaker
	now        func() time.Time
	transition metric.Int64Counter
}

func NewClient() *Client {
	return &Client{http: &http.Client{Transport: platform.InstrumentTransport(http.DefaultTransport)}}
}

func NewProtectedClient(partners []platform.Partner, cfg platform.Resilience) *Client {
	client := NewClient()
	client.http.Timeout = cfg.PartnerTimeout
	client.now = time.Now
	client.circuits = make(map[string]*breaker, len(partners))
	for _, p := range partners {
		client.circuits[p.Name] = newBreaker(cfg.FailureLimit, cfg.FailureWindow, cfg.OpenTime)
	}
	client.transition, _ = otel.Meter("quotation-api").Int64Counter("quote_breaker_transitions")
	return client
}

const responseLimit = 1 << 20

func (c *Client) Quote(ctx context.Context, p platform.Partner, request any) (Quote, error) {
	circuit := c.circuits[p.Name]
	if circuit == nil {
		return c.quote(ctx, p, request)
	}
	if err := ctx.Err(); err != nil {
		return Quote{}, err
	}

	span := trace.SpanFromContext(ctx)
	onChange := func(from, to state) {
		attrs := []attribute.KeyValue{
			attribute.String("partner", p.Name),
			attribute.String("from_state", string(from)),
			attribute.String("to_state", string(to)),
		}
		c.transition.Add(ctx, 1, metric.WithAttributes(attrs...))
		span.AddEvent("quote.breaker.transition", trace.WithAttributes(attrs...))
	}

	generation, allowed := circuit.admit(c.now(), onChange)
	if !allowed {
		span.AddEvent("quote.breaker.rejected", trace.WithAttributes(attribute.String("partner", p.Name)))
		return Quote{}, ErrCircuitUnavailable
	}
	quote, err := c.quote(ctx, p, request)
	circuit.complete(generation, classify(ctx, err), c.now(), onChange)
	return quote, err
}

func classify(ctx context.Context, err error) outcome {
	if err == nil {
		return succeeded
	}
	if ctx.Err() != nil {
		return ignored
	}
	var failure *Error
	if errors.As(err, &failure) && failure.Status >= 400 && failure.Status < 500 && failure.Status != 429 {
		return ignored
	}
	return failed
}

func (c *Client) quote(ctx context.Context, p platform.Partner, request any) (Quote, error) {
	body, err := json.Marshal(request)
	if err != nil {
		return Quote{}, &Error{Partner: p.Name, Reason: fmt.Sprintf("invalid request: %v", err)}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.BaseURL+"/quotes", bytes.NewReader(body))
	if err != nil {
		return Quote{}, &Error{Partner: p.Name, Reason: err.Error()}
	}
	req.Header.Set("Content-Type", "application/json")

	response, err := c.http.Do(req)
	if err != nil {
		return Quote{}, &Error{Partner: p.Name, Reason: err.Error()}
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, responseLimit))
		return Quote{}, &Error{
			Partner: p.Name,
			Status:  response.StatusCode,
			Reason:  fmt.Sprintf("partner replied %d", response.StatusCode),
		}
	}

	var quote Quote
	if err := json.NewDecoder(io.LimitReader(response.Body, responseLimit)).Decode(&quote); err != nil {
		return Quote{}, &Error{Partner: p.Name, Reason: fmt.Sprintf("unreadable response: %v", err)}
	}

	quote.Partner = p.Name
	return quote, nil
}

type Error struct {
	Partner string
	Status  int
	Reason  string
}

func (e *Error) Error() string {
	return fmt.Sprintf("partner %s: %s", e.Partner, e.Reason)
}
