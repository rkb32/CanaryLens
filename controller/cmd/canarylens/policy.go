package main

func rollbackRequired(errorRate, threshold float64) bool {
	return errorRate > threshold
}

type rolloutDecision struct {
	Rollback bool
	Complete bool
	Weight   int
}

func evaluateRollout(errorRate, threshold float64, current int, steps []int) rolloutDecision {
	if rollbackRequired(errorRate, threshold) {
		return rolloutDecision{Rollback: true}
	}
	next, ok := nextCanaryWeight(current, steps)
	if !ok {
		return rolloutDecision{Complete: true, Weight: 100}
	}
	return rolloutDecision{Weight: next}
}

func nextCanaryWeight(current int, steps []int) (int, bool) {
	for _, step := range steps {
		if step > current {
			return step, true
		}
	}
	return 0, false
}
