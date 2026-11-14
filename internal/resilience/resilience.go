// Package resilience retries rate-limited, overloaded and dropped HTTP calls.
package resilience

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"strconv"
	"time"
)

// drainLimit caps how much of a retried body is read, so its connection is reused.
const drainLimit = 1 << 20

// Policy decides when and how often a call is retried.
type Policy struct {
	// MaxAttempts includes the first attempt.
	MaxAttempts    int
	InitialBackoff time.Duration
	MaxBackoff     time.Duration
	// Jitter shortens each backoff by a random share up to this, 0 to 1.
	Jitter float64
	// RetryStatus picks the statuses to retry; nil retries none.
	RetryStatus func(status int) bool

	// Test hooks; nil uses the real timer, clock and random source.
	wait   func(ctx context.Context, d time.Duration) error
	now    func() time.Time
	random func() float64
}

// DefaultPolicy retries the way TypeSafe's SDK does.
func DefaultPolicy() Policy {
	return Policy{
		MaxAttempts:    5,
		InitialBackoff: 500 * time.Millisecond,
		MaxBackoff:     5 * time.Second,
		Jitter:         0.25,
		RetryStatus:    Transient,
	}
}

// Transient reports whether a status is worth retrying.
func Transient(status int) bool {
	return status == http.StatusRequestTimeout || status == http.StatusTooManyRequests || status >= http.StatusInternalServerError
}

// Send makes one attempt, with a new request each time.
type Send func(ctx context.Context) (*http.Response, error)

// Do retries send until it gets a response worth keeping; the caller closes its body.
// If the last attempt still gets a retryable status, Do returns an *ExhaustedError.
func (p Policy) Do(ctx context.Context, send Send) (*http.Response, error) {
	attempts := max(p.MaxAttempts, 1)
	for attempt := 1; ; attempt++ {
		resp, err := send(ctx)
		if ctxErr := ctx.Err(); err != nil && ctxErr != nil {
			return nil, ctxErr
		}
		if err == nil && (p.RetryStatus == nil || !p.RetryStatus(resp.StatusCode)) {
			return resp, nil
		}
		if attempt == attempts {
			return nil, exhausted(attempts, resp, err)
		}

		var delay time.Duration
		if err != nil {
			delay = p.backoff(attempt)
			slog.Info("Retrying HTTP call", "error", err, "attempt", attempt, "max_attempts", attempts, "delay", delay)
		} else {
			delay = p.retryDelay(attempt, resp.Header.Get("Retry-After"))
			slog.Info("Retrying HTTP call", "status", resp.StatusCode, "attempt", attempt, "max_attempts", attempts, "delay", delay)
			drain(resp.Body)
		}
		if err := p.pause(ctx, delay); err != nil {
			return nil, err
		}
	}
}

// ExhaustedError holds the last response after every attempt was retried.
type ExhaustedError struct {
	Attempts int
	Status   int
	Header   http.Header
	Body     []byte
}

func (e *ExhaustedError) Error() string {
	return fmt.Sprintf("status %d after %d attempts: %s", e.Status, e.Attempts, e.Body)
}

func exhausted(attempts int, resp *http.Response, err error) error {
	if err != nil {
		return fmt.Errorf("%d attempts failed, the last: %w", attempts, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, drainLimit))
	return &ExhaustedError{Attempts: attempts, Status: resp.StatusCode, Header: resp.Header, Body: body}
}

func drain(body io.ReadCloser) {
	_, _ = io.Copy(io.Discard, io.LimitReader(body, drainLimit))
	_ = body.Close()
}

func (p Policy) retryDelay(attempt int, retryAfter string) time.Duration {
	if seconds, err := strconv.Atoi(retryAfter); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second
	}
	if at, err := http.ParseTime(retryAfter); err == nil {
		return max(at.Sub(p.clock()), 0)
	}
	return p.backoff(attempt)
}

func (p Policy) backoff(attempt int) time.Duration {
	delay := p.InitialBackoff
	for range attempt - 1 {
		if delay >= p.MaxBackoff {
			break
		}
		delay *= 2
	}
	delay = min(delay, p.MaxBackoff)
	return delay - time.Duration(p.Jitter*p.draw()*float64(delay))
}

func (p Policy) pause(ctx context.Context, d time.Duration) error {
	if p.wait != nil {
		return p.wait(ctx, d)
	}
	return sleep(ctx, d)
}

func (p Policy) clock() time.Time {
	if p.now != nil {
		return p.now()
	}
	return time.Now()
}

func (p Policy) draw() float64 {
	if p.random != nil {
		return p.random()
	}
	return rand.Float64()
}

func sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
