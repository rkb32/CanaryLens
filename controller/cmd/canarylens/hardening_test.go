package main

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestRolloutStepsFromDecodedCustomResource(t *testing.T) {
	defaults := []int{5, 25, 50, 100}
	decode := func(spec string) map[string]any {
		t.Helper()
		data := []byte(`{"apiVersion":"canarylens.io/v1alpha1","kind":"CanaryRollout","metadata":{"name":"demo"},"spec":` + spec + `}`)
		obj, _, err := unstructured.UnstructuredJSONScheme.Decode(data, nil, nil)
		if err != nil {
			t.Fatalf("decode: %v", err)
		}
		out, _, _ := unstructured.NestedMap(obj.(*unstructured.Unstructured).Object, "spec")
		return out
	}

	if got, want := rolloutSteps(decode(`{"steps":[60,30,10,100]}`), defaults), []int{10, 30, 60, 100}; !reflect.DeepEqual(got, want) {
		t.Fatalf("rolloutSteps(spec.steps) = %v, want %v", got, want)
	}
	if got := rolloutSteps(decode(`{}`), defaults); !reflect.DeepEqual(got, defaults) {
		t.Fatalf("rolloutSteps(no steps) = %v, want defaults %v", got, defaults)
	}
	if got := rolloutSteps(decode(`{"steps":[0,101]}`), defaults); len(got) != 0 {
		t.Fatalf("rolloutSteps(out of range) = %v, want none", got)
	}
}

func TestParseErrorThresholdRejectsInvalidValues(t *testing.T) {
	for _, raw := range []string{"NaN", "Inf", "+Inf", "-Inf", "0", "-0.01", "1.5", "1e400"} {
		if got, err := parseErrorThreshold(raw); err == nil {
			t.Errorf("parseErrorThreshold(%q) = %v, want an error", raw, got)
		}
	}
}

func TestParseErrorThresholdAcceptsRange(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want float64
	}{
		{raw: "0.01", want: 0.01},
		{raw: "1", want: 1},
	} {
		got, err := parseErrorThreshold(tc.raw)
		if err != nil || got != tc.want {
			t.Errorf("parseErrorThreshold(%q) = (%v, %v), want (%v, nil)", tc.raw, got, err, tc.want)
		}
	}
}

func TestRolloutStepsAreSortedBeforeUse(t *testing.T) {
	steps := parseRolloutSteps("50,25,5,100")
	if want := []int{5, 25, 50, 100}; !reflect.DeepEqual(steps, want) {
		t.Fatalf("parseRolloutSteps() = %v, want %v", steps, want)
	}
	got, ok := nextCanaryWeight(0, steps)
	if !ok || got != 5 {
		t.Fatalf("nextCanaryWeight(0) = (%d, %v), want (5, true)", got, ok)
	}
}

func TestCORSPreflightAllowsAuthorizationHeader(t *testing.T) {
	t.Setenv("CORS_ORIGINS", "http://localhost:3000")
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	handler := cors(next)

	req := httptest.NewRequest(http.MethodOptions, "/api/rollouts", nil)
	req.Header.Set("Origin", "http://localhost:3000")
	req.Header.Set("Access-Control-Request-Method", "GET")
	req.Header.Set("Access-Control-Request-Headers", "authorization")
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)

	if got := res.Header().Get("Access-Control-Allow-Origin"); got != "http://localhost:3000" {
		t.Fatalf("Access-Control-Allow-Origin = %q, want the allowlisted origin", got)
	}
	allowed := res.Header().Get("Access-Control-Allow-Headers")
	found := false
	for _, h := range strings.Split(allowed, ",") {
		if strings.EqualFold(strings.TrimSpace(h), "Authorization") {
			found = true
		}
	}
	if !found {
		t.Fatalf("Access-Control-Allow-Headers = %q, want it to include Authorization", allowed)
	}
}
