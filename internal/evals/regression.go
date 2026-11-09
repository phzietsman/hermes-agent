package evals

import (
	"context"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/phzietsman/hermes-agent/internal/memory"
	"github.com/phzietsman/hermes-agent/internal/memory/sam"
)

// Topics are the things the latency check remembers and asks about.
var Topics = []string{"staging cluster", "on-call channel", "release branch", "VPN profile", "billing database",
	"Jira project", "package registry", "build server", "laptop asset tag", "current sprint"}

// LatencyStats summarises a series of timings in milliseconds.
type LatencyStats struct {
	N   float64 `json:"n"`
	P50 float64 `json:"p50"`
	P95 float64 `json:"p95"`
	Max float64 `json:"max"`
}

// Stats computes the summary the Python harness reports (p95 by nearest rank over n-1).
func Stats(ms []float64) LatencyStats {
	if len(ms) == 0 {
		return LatencyStats{}
	}
	o := append([]float64(nil), ms...)
	sort.Float64s(o)
	med := o[len(o)/2]
	if len(o)%2 == 0 {
		med = (o[len(o)/2-1] + o[len(o)/2]) / 2
	}
	p95 := o[min(len(o)-1, int(math.Round(0.95*float64(len(o)-1))))]
	r := func(f float64) float64 { return math.Round(f*10) / 10 }
	return LatencyStats{N: float64(len(ms)), P50: r(med), P95: r(p95), Max: r(o[len(o)-1])}
}

// Regression is the result of scenario 8.
type Regression struct {
	RawRecall     LatencyStats `json:"raw_recall_ms"`
	PrefetchCold  LatencyStats `json:"prefetch_cold_ms"`
	PrefetchWarm  LatencyStats `json:"prefetch_warm_ms"`
	PrefetchWaitS float64      `json:"prefetch_wait_s"`
	GateP95Ms     float64      `json:"gate_p95_ms"`
}

func timed(f func()) float64 {
	t0 := time.Now()
	f()
	return float64(time.Since(t0).Microseconds()) / 1000
}

// MeasureLatency is scenario 8 (live part): recall and prefetch latency through the real provider against a real
// sandbox. It reports the raw service recall, the blocking part of Prefetch cold (no warm-up) and warm (QueuePrefetch
// issued one model turn earlier), because the user-visible cost is the blocking part. s must carry run-unique
// scopes; they are registered here.
func MeasureLatency(ctx context.Context, s *sam.Settings, home string, n, warmN int, warmGap time.Duration) (*Regression, error) {
	c := s.Client()
	for _, scope := range []string{s.DefaultScope, s.AgentScope} {
		if err := c.RegisterScope(ctx, scope); err != nil {
			return nil, fmt.Errorf("register scope %s: %w", scope, err)
		}
	}
	for i, topic := range Topics {
		if _, err := c.Remember(ctx, fmt.Sprintf("Our %s is item-%d.", topic, 100+i), sam.RememberOptions{Scopes: s.DefaultScope}); err != nil {
			return nil, err
		}
	}
	topic := func(i int) string { return Topics[i%len(Topics)] }
	var raw []float64
	for i := range n {
		q := fmt.Sprintf("What is our %s?", topic(i))
		raw = append(raw, timed(func() { _, _ = c.Recall(ctx, q, s.TopK, nil) }))
	}
	p := memory.New(ctx, s, memory.Options{Home: home, SessionID: "regression"})
	defer p.Close()
	var cold, warm []float64
	for i := range n {
		q := fmt.Sprintf("What is our %s, again?", topic(i))
		cold = append(cold, timed(func() { p.Prefetch(ctx, q) }))
	}
	for i := range warmN {
		q := fmt.Sprintf("Remind me what our %s is.", topic(i))
		p.QueuePrefetch(q)
		select {
		case <-time.After(warmGap): // stands in for the model's own turn time
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		warm = append(warm, timed(func() { p.Prefetch(ctx, q) }))
	}
	return &Regression{RawRecall: Stats(raw), PrefetchCold: Stats(cold), PrefetchWarm: Stats(warm),
		PrefetchWaitS: s.PrefetchWait.Seconds(), GateP95Ms: 1500}, nil
}
