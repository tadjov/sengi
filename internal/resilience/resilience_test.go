package resilience

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTransient_RetriesTimeoutsRateLimitsAndServerErrors(t *testing.T) {
	tests := map[int]bool{
		http.StatusOK:                    false,
		http.StatusBadRequest:            false,
		http.StatusUnauthorized:          false,
		http.StatusNotFound:              false,
		http.StatusRequestTimeout:        true,
		http.StatusRequestEntityTooLarge: false,
		http.StatusUnsupportedMediaType:  false,
		http.StatusUnprocessableEntity:   false,
		http.StatusTooManyRequests:       true,
		http.StatusInternalServerError:   true,
		http.StatusBadGateway:            true,
		http.StatusServiceUnavailable:    true,
		529:                              true,
	}
	for status, want := range tests {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			assert.Equal(t, want, Transient(status))
		})
	}
}

// Do hands back the first response it does not retry, its body unread for
// the caller, a refused request as much as a success.
func TestDo_ReturnsTheFirstResponseItDoesNotRetry(t *testing.T) {
	tests := map[string]struct {
		replies    []reply
		wantStatus int
		wantBody   string
		wantSent   int
	}{
		"a success after two transient failures": {
			replies:    []reply{{status: 503}, {status: 429}, {status: 200, body: "graded"}},
			wantStatus: 200, wantBody: "graded", wantSent: 3,
		},
		"a refused request": {
			replies:    []reply{{status: 503}, {status: 422, body: "bad field"}, {status: 200}},
			wantStatus: 422, wantBody: "bad field", wantSent: 2,
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			server := &scriptedServer{replies: tt.replies}

			resp, err := testPolicy(nil).Do(context.Background(), server.send)

			require.NoError(t, err)
			defer resp.Body.Close()
			assert.Equal(t, tt.wantStatus, resp.StatusCode)
			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			assert.Equal(t, tt.wantBody, string(body))
			assert.Equal(t, tt.wantSent, server.sent)
		})
	}
}

// Every retried response is read to its end and closed, so its connection
// can be reused; the one Do returns stays open for the caller.
func TestDo_DrainsAndClosesEveryRetriedBody(t *testing.T) {
	server := &scriptedServer{replies: []reply{{status: 503, body: "busy"}, {status: 429, body: "slow down"}, {status: 200, body: "graded"}}}

	resp, err := testPolicy(nil).Do(context.Background(), server.send)

	require.NoError(t, err)
	defer resp.Body.Close()
	require.Len(t, server.bodies, 3)
	for _, retried := range server.bodies[:2] {
		assert.True(t, retried.drained, "retried body read to its end")
		assert.True(t, retried.closed, "retried body closed")
	}
	assert.False(t, server.bodies[2].closed, "the returned body is the caller's to close")
	assert.Same(t, server.bodies[2], resp.Body)
}

// Without a retry-after, the wait starts at the initial backoff and doubles up
// to the cap.
func TestDo_BacksOffDoublingFromTheInitialDelayToTheCap(t *testing.T) {
	var waits []time.Duration
	policy := testPolicy(&waits)
	policy.MaxAttempts = 6
	server := &scriptedServer{replies: []reply{{status: 503}}}

	resp, err := policy.Do(context.Background(), server.send)
	closeIfAny(resp)

	assert.Nil(t, resp, "no response with an error")
	require.Error(t, err)
	assert.Equal(t, []time.Duration{500 * time.Millisecond, time.Second, 2 * time.Second, 4 * time.Second, 5 * time.Second}, waits)
}

// Jitter takes a random share of up to Jitter off each backoff.
func TestDo_JitterShortensEachBackoffByUpToItsShare(t *testing.T) {
	var waits []time.Duration
	policy := testPolicy(&waits)
	policy.Jitter = 0.25
	policy.random = func() float64 { return 0.5 }
	server := &scriptedServer{replies: []reply{{status: 503}}}

	resp, err := policy.Do(context.Background(), server.send)
	closeIfAny(resp)

	assert.Nil(t, resp, "no response with an error")
	require.Error(t, err)
	// 0.5 s and 1 s, each less 0.25 × 0.5 of itself.
	assert.Equal(t, []time.Duration{437500 * time.Microsecond, 875 * time.Millisecond}, waits)
}

// A retry-after in seconds or as an HTTP date is waited as given, without
// jitter; one Do cannot read falls back to the backoff.
func TestDo_WaitsWhatRetryAfterAsks(t *testing.T) {
	now := time.Date(2026, 11, 14, 22, 0, 0, 0, time.UTC)
	tests := map[string]struct {
		retryAfter string
		want       time.Duration
	}{
		"seconds":           {retryAfter: "2", want: 2 * time.Second},
		"zero seconds":      {retryAfter: "0", want: 0},
		"an HTTP date":      {retryAfter: "Sat, 14 Nov 2026 22:00:03 GMT", want: 3 * time.Second},
		"a date gone by":    {retryAfter: "Sat, 14 Nov 2026 21:59:00 GMT", want: 0},
		"negative seconds":  {retryAfter: "-1", want: 437500 * time.Microsecond},
		"neither":           {retryAfter: "soon", want: 437500 * time.Microsecond},
		"fractional second": {retryAfter: "1.5", want: 437500 * time.Microsecond},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			var waits []time.Duration
			policy := testPolicy(&waits)
			policy.MaxAttempts = 2
			policy.Jitter = 0.25
			policy.random = func() float64 { return 0.5 }
			policy.now = func() time.Time { return now }
			server := &scriptedServer{replies: []reply{
				{status: 429, header: http.Header{"Retry-After": {tt.retryAfter}}},
				{status: 200},
			}}

			resp, err := policy.Do(context.Background(), server.send)

			require.NoError(t, err)
			defer resp.Body.Close()
			assert.Equal(t, []time.Duration{tt.want}, waits)
		})
	}
}

// When every attempt is retryable, Do gives up after MaxAttempts with an
// error holding the last response.
func TestDo_GivesUpAfterMaxAttemptsNamingTheLastStatus(t *testing.T) {
	server := &scriptedServer{replies: []reply{
		{status: 503, header: http.Header{"X-Request-Id": {"req-1"}}, body: "busy"},
		{status: 529, header: http.Header{"X-Request-Id": {"req-2"}}, body: "overloaded"},
	}}

	resp, err := testPolicy(nil).Do(context.Background(), server.send)
	closeIfAny(resp)

	assert.Nil(t, resp, "no response with an error")
	var exhausted *ExhaustedError
	require.ErrorAs(t, err, &exhausted)
	assert.Equal(t, 3, exhausted.Attempts)
	assert.Equal(t, 529, exhausted.Status)
	assert.Equal(t, "req-2", exhausted.Header.Get("X-Request-Id"))
	assert.Equal(t, "overloaded", string(exhausted.Body))
	assert.Contains(t, err.Error(), "529")
	assert.Equal(t, 3, server.sent)
	for _, body := range server.bodies {
		assert.True(t, body.closed)
	}
}

var errReset = errors.New("connection reset by peer")

// A request that never got a response is retried as a transient status is.
func TestDo_RetriesATransportError(t *testing.T) {
	server := &scriptedServer{replies: []reply{{err: errReset}, {status: 200, body: "graded"}}}

	resp, err := testPolicy(nil).Do(context.Background(), server.send)

	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, 200, resp.StatusCode)
}

func TestDo_GivesUpOnATransportErrorEveryAttempt(t *testing.T) {
	server := &scriptedServer{replies: []reply{{err: errReset}}}

	resp, err := testPolicy(nil).Do(context.Background(), server.send)
	closeIfAny(resp)

	assert.Nil(t, resp, "no response with an error")
	assert.ErrorIs(t, err, errReset)
	assert.Equal(t, 3, server.sent)
}

// A cancelled call is not retried: Do returns the context's error, whether
// the cancel lands during a wait or fails the request itself.
func TestDo_ReturnsTheContextErrorOnceCancelled(t *testing.T) {
	t.Run("during a wait", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		policy := testPolicy(nil)
		policy.wait = func(ctx context.Context, d time.Duration) error {
			cancel()
			return sleep(ctx, d)
		}
		server := &scriptedServer{replies: []reply{{status: 529, header: http.Header{"Retry-After": {"3600"}}}}}

		resp, err := policy.Do(ctx, server.send)
		closeIfAny(resp)

		assert.Nil(t, resp, "no response with an error")
		assert.Equal(t, context.Canceled, err)
		assert.Equal(t, 1, server.sent)
		assert.True(t, server.bodies[0].closed)
	})
	t.Run("failing the request", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		sent := 0
		send := func(ctx context.Context) (*http.Response, error) {
			sent++
			cancel()
			return nil, errors.New("net/http: request canceled")
		}

		resp, err := testPolicy(nil).Do(ctx, send)
		closeIfAny(resp)

		assert.Nil(t, resp, "no response with an error")
		assert.Equal(t, context.Canceled, err)
		assert.Equal(t, 1, sent)
	})
}

// The default retries as TypeSafe's SDK does: 408, 429 and any 5xx, backing
// off from 0.5 s, doubling, over 5 attempts.
func TestDefaultPolicy_RetriesAsTypeSafesSDK(t *testing.T) {
	var waits []time.Duration
	policy := DefaultPolicy()
	policy.wait = recordWaits(&waits)
	policy.random = func() float64 { return 0 }
	server := &scriptedServer{replies: []reply{{status: 408}, {status: 429}, {status: 500}, {status: 529}, {status: 503}}}

	resp, err := policy.Do(context.Background(), server.send)
	closeIfAny(resp)

	assert.Nil(t, resp, "no response with an error")
	var exhausted *ExhaustedError
	require.ErrorAs(t, err, &exhausted)
	assert.Equal(t, 503, exhausted.Status)
	assert.Equal(t, 5, server.sent)
	assert.Equal(t, []time.Duration{500 * time.Millisecond, time.Second, 2 * time.Second, 4 * time.Second}, waits)
}

func closeIfAny(resp *http.Response) {
	if resp != nil {
		resp.Body.Close()
	}
}

// testPolicy makes three attempts, backing off from 0.5 s to 5 s without
// jitter, and records each wait in waits instead of sleeping.
func testPolicy(waits *[]time.Duration) Policy {
	if waits == nil {
		waits = new([]time.Duration)
	}
	return Policy{
		MaxAttempts:    3,
		InitialBackoff: 500 * time.Millisecond,
		MaxBackoff:     5 * time.Second,
		RetryStatus:    Transient,
		wait:           recordWaits(waits),
		random:         func() float64 { return 0 },
		now:            func() time.Time { panic("the clock is read only for an HTTP-date retry-after") },
	}
}

func recordWaits(waits *[]time.Duration) func(context.Context, time.Duration) error {
	return func(ctx context.Context, d time.Duration) error {
		*waits = append(*waits, d)
		return ctx.Err()
	}
}

type reply struct {
	status int
	header http.Header
	body   string
	err    error
}

// scriptedServer answers the nth attempt with the nth reply, repeating the
// last, and keeps every body it hands out.
type scriptedServer struct {
	replies []reply
	sent    int
	bodies  []*trackedBody
}

func (s *scriptedServer) send(context.Context) (*http.Response, error) {
	r := s.replies[min(s.sent, len(s.replies)-1)]
	s.sent++
	if r.err != nil {
		return nil, r.err
	}
	body := &trackedBody{reader: strings.NewReader(r.body)}
	s.bodies = append(s.bodies, body)
	header := r.header
	if header == nil {
		header = http.Header{}
	}
	return &http.Response{StatusCode: r.status, Header: header, Body: body}, nil
}

type trackedBody struct {
	reader  *strings.Reader
	drained bool
	closed  bool
}

func (b *trackedBody) Read(p []byte) (int, error) {
	n, err := b.reader.Read(p)
	if errors.Is(err, io.EOF) {
		b.drained = true
	}
	return n, err
}

func (b *trackedBody) Close() error {
	b.closed = true
	return nil
}
