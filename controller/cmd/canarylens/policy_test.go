package main

import "testing"

func TestRollbackThresholdIsStrict(t *testing.T) {
	for _, tc := range []struct {
		rate, threshold float64
		want            bool
	}{
		{rate: 0, threshold: .01, want: false},
		{rate: .01, threshold: .01, want: false},
		{rate: .01001, threshold: .01, want: true},
		{rate: .035, threshold: .01, want: true},
	} {
		if got := rollbackRequired(tc.rate, tc.threshold); got != tc.want {
			t.Errorf("rollbackRequired(%v, %v) = %v, want %v", tc.rate, tc.threshold, got, tc.want)
		}
	}
}

func TestCanaryStepProgression(t *testing.T) {
	steps := []int{5, 25, 50, 100}
	for _, tc := range []struct {
		current, want int
		ok            bool
	}{
		{current: 0, want: 5, ok: true},
		{current: 5, want: 25, ok: true},
		{current: 50, want: 100, ok: true},
		{current: 100, want: 0, ok: false},
	} {
		got, ok := nextCanaryWeight(tc.current, steps)
		if got != tc.want || ok != tc.ok {
			t.Errorf("nextCanaryWeight(%d) = (%d, %v), want (%d, %v)", tc.current, got, ok, tc.want, tc.ok)
		}
	}
}

func TestTwentyFiveBadReleaseDecisionsRollback(t *testing.T) {
	const count = 25
	for i := 0; i < count; i++ {
		decision := evaluateRollout(.035, .01, 5, []int{5, 25, 50, 100})
		if !decision.Rollback || decision.Weight != 0 {
			t.Fatalf("bad release %d was not rolled back", i+1)
		}
	}
}

func TestEvaluateRolloutAdvancesAndCompletes(t *testing.T) {
	steps := []int{5, 25, 50, 100}
	first := evaluateRollout(.002, .01, 0, steps)
	if first.Rollback || first.Complete || first.Weight != 5 {
		t.Fatalf("unexpected first decision: %+v", first)
	}
	last := evaluateRollout(.002, .01, 100, steps)
	if last.Rollback || !last.Complete || last.Weight != 100 {
		t.Fatalf("unexpected terminal decision: %+v", last)
	}
}
