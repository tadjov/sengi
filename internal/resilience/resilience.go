// Package resilience retries HTTP calls that failed for a reason a later
// attempt may not meet: a rate limit, an overloaded server, a dropped
// connection.
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

// drainLimit bounds how much of a retried body is read so its connection can
// be reused.
const drainLimit = 1 << 20

// Policy is how often, after how long and on which statuses a call is retried.
type Policy struct {
	// MaxAttempts counts the first attempt; below 1 means 1.
	MaxAttempts    int
	InitialBackoff time.Duration
	MaxBackoff     time.Duration
	// Jitter is the share of each backoff drawn at random, 0 to 1: 0.25 waits
	// between 75% and 100% of it. A Retry-After is waited as given.
	Jitter float64
	// RetryStatus reports whether a response with status is worth another
	// attempt; nil retries none.
	RetryStatus func(status int) bool

	// nil means the real timer, clock and random source.
	wait   func(ctx context.Context, d time.Duration) error
	now    func() time.Time
	random func() float64
}

// DefaultPolicy retries as TypeSafe's SDK does: 408, 429 and any 5xx, backing
// off from 0.5 s, doubling up to 5 s, over 5 attempts.
func DefaultPolicy() Policy {
	return Policy{
		MaxAttempts:    5,
		InitialBackoff: 500 * time.Millisecond,
		MaxBackoff:     5 * time.Second,
		Jitter:         0.25,
		RetryStatus:    Transient,
	}
}

// Transient reports a status a later attempt may not meet: a timeout, a rate
// limit, or a server error, 529 overloaded included.
func Transient(status int) bool {
	return status == http.StatusRequestTimeout || status == http.StatusTooManyRequests || status >= http.StatusInternalServerError
}

// Send makes one attempt of a call. It must build a new request each time,
// since an attempt spends its request's body.
type Send func(ctx context.Context) (*http.Response, error)

// Do runs send until it returns a response RetryStatus does not retry, and
// returns that response with its body unread for the caller to close. A
// request that got no response is retried too. Once ctx is done, Do returns
// ctx.Err(). When the attempts run out, the error is an *ExhaustedError
// holding the last response, or the last transport error.
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

// ExhaustedError is the last response of a call whose every attempt was
// retried.
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

// retryDelay waits what a Retry-After in whole seconds or an HTTP date asks,
// else the backoff.
func (p Policy) retryDelay(attempt int, retryAfter string) time.Duration {
	if seconds, err := strconv.Atoi(retryAfter); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second
	}
	if at, err := http.ParseTime(retryAfter); err == nil {
		return max(at.Sub(p.clock()), 0)
	}
	return p.backoff(attempt)
}

// backoff doubles from InitialBackoff up to MaxBackoff, less its jitter.
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
