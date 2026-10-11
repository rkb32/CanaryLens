package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

var rolloutGVR = schema.GroupVersionResource{Group: "canarylens.io", Version: "v1alpha1", Resource: "canaryrollouts"}

// rolloutSteps returns a CanaryRollout's spec.steps sorted ascending, or the
// controller defaults when the resource sets none. The unstructured decoder
// yields int64 for whole numbers, so both int64 and float64 values are read.
func rolloutSteps(spec map[string]any, defaults []int) []int {
	raw, ok := spec["steps"].([]any)
	if !ok || len(raw) == 0 {
		return defaults
	}
	var steps []int
	for _, value := range raw {
		var step int
		switch v := value.(type) {
		case int64:
			step = int(v)
		case float64:
			step = int(v)
		default:
			continue
		}
		if step > 0 && step <= 100 {
			steps = append(steps, step)
		}
	}
	sort.Ints(steps)
	return steps
}

func (a *App) watchKubernetes(ctx context.Context) {
	config, err := rest.InClusterConfig()
	if err != nil {
		log.Fatalf("kubernetes mode needs in-cluster credentials: %v", err)
	}
	dyn, err := dynamic.NewForConfig(config)
	if err != nil {
		log.Fatalf("create dynamic client: %v", err)
	}
	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		log.Fatalf("create kubernetes client: %v", err)
	}
	period := a.interval
	if period < 5*time.Second {
		period = 5 * time.Second
	}
	ticker := time.NewTicker(period)
	defer ticker.Stop()
	log.Printf("watching CanaryRollout resources in namespace %q", env("NAMESPACE", "default"))
	for {
		if err := a.reconcileRollouts(ctx, dyn, client); err != nil {
			log.Printf("reconcile rollouts: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (a *App) reconcileRollouts(ctx context.Context, dyn dynamic.Interface, client kubernetes.Interface) error {
	ns := env("NAMESPACE", "default")
	list, err := dyn.Resource(rolloutGVR).Namespace(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	for i := range list.Items {
		if err := a.reconcileOne(ctx, dyn, client, &list.Items[i]); err != nil {
			log.Printf("%s/%s: %v", ns, list.Items[i].GetName(), err)
		}
	}
	return nil
}

func (a *App) reconcileOne(ctx context.Context, dyn dynamic.Interface, client kubernetes.Interface, obj *unstructured.Unstructured) error {
	ns, name := obj.GetNamespace(), obj.GetName()
	spec, _, _ := unstructured.NestedMap(obj.Object, "spec")
	get := func(key string) string { value, _ := spec[key].(string); return value }
	service := get("serviceName")
	if service == "" {
		service = name
	}
	stable, canary, ingress := get("stableService"), get("canaryService"), get("ingressName")
	if stable == "" || canary == "" || ingress == "" {
		return fmt.Errorf("spec needs stableService, canaryService, and ingressName")
	}
	status, _, _ := unstructured.NestedMap(obj.Object, "status")
	phase, _ := status["phase"].(string)
	if phase == "Succeeded" || phase == "RolledBack" {
		return nil
	}
	weight, _, _ := unstructured.NestedInt64(obj.Object, "status", "trafficWeight")
	steps := rolloutSteps(spec, a.steps)
	if len(steps) == 0 {
		return fmt.Errorf("spec.steps must contain values from 1 to 100")
	}
	for _, svc := range []string{stable, canary} {
		ready, err := hasReadyEndpoints(ctx, client, ns, svc)
		if err != nil {
			return err
		}
		if !ready {
			return a.waitingStatus(ctx, dyn, obj, service, int(weight), "WaitingForEndpoints", fmt.Sprintf("Waiting for ready endpoints on Service %q", svc))
		}
	}
	stableMetrics, err := a.prometheusMetrics(ctx, stable)
	if err != nil {
		return err
	}
	canaryMetrics, err := a.prometheusMetrics(ctx, canary)
	if err != nil {
		return err
	}
	rate := canaryMetrics["error_rate"]
	ro := &Rollout{ID: ns + "/" + name, Service: service, Scenario: "kubernetes", Phase: "Progressing", Weight: int(weight), ErrorRate: rate, StartedAt: obj.GetCreationTimestamp().Time, UpdatedAt: time.Now().UTC()}
	if stableMetrics["request_rate"] <= 0 || (weight > 0 && canaryMetrics["request_rate"] <= 0) {
		return a.waitingStatus(ctx, dyn, obj, service, int(weight), "WaitingForMetrics", "Waiting for stable and canary request metrics before changing traffic")
	}
	ro.StableLatencyMs = stableMetrics["latency_ms"]
	ro.CanaryLatencyMs = canaryMetrics["latency_ms"]
	a.event(ctx, ro, "sample", fmt.Sprintf("Metric check: %.2f%% canary 5xx, stable/canary latency %.0f/%.0f ms", rate*100, ro.StableLatencyMs, ro.CanaryLatencyMs), ro.Weight, rate)
	score, reason, scoreErr := a.score(ctx, stableMetrics, canaryMetrics)
	if scoreErr != nil {
		reason = "Scoring service unavailable; Prometheus threshold remains authoritative"
	}
	decision := evaluateRollout(rate, a.threshold, int(weight), steps)
	if decision.Rollback {
		if err := setCanaryWeight(ctx, client, ns, ingress, 0); err != nil {
			return err
		}
		ro.Phase = "RolledBack"
		ro.Weight = 0
		ro.Reason = fmt.Sprintf("Canary error rate %.2f%% exceeded %.2f%%; traffic returned to stable", rate*100, a.threshold*100)
		a.event(ctx, ro, "rollback", ro.Reason, 0, rate)
	} else {
		if decision.Complete {
			ro.Phase = "Succeeded"
			ro.Weight = 100
			ro.Reason = "All rollout steps passed; canary reached 100% traffic"
			if int(weight) != 100 {
				if err := setCanaryWeight(ctx, client, ns, ingress, 100); err != nil {
					return err
				}
				a.event(ctx, ro, "completed", ro.Reason, 100, rate)
			}
		} else {
			ro.Weight = decision.Weight
			ro.Reason = reason
			if err := setCanaryWeight(ctx, client, ns, ingress, ro.Weight); err != nil {
				return err
			}
			a.event(ctx, ro, "progressed", fmt.Sprintf("Metrics healthy at %.2f%% errors; advanced canary traffic to %d%%", rate*100, ro.Weight), ro.Weight, rate)
		}
	}
	ro.Score = score
	ro.UpdatedAt = time.Now().UTC()
	_ = unstructured.SetNestedField(obj.Object, ro.Phase, "status", "phase")
	_ = unstructured.SetNestedField(obj.Object, int64(ro.Weight), "status", "trafficWeight")
	_ = unstructured.SetNestedField(obj.Object, rate, "status", "errorRate")
	_ = unstructured.SetNestedField(obj.Object, score, "status", "score")
	_ = unstructured.SetNestedField(obj.Object, ro.StableLatencyMs, "status", "stableLatencyMs")
	_ = unstructured.SetNestedField(obj.Object, ro.CanaryLatencyMs, "status", "canaryLatencyMs")
	_ = unstructured.SetNestedField(obj.Object, ro.Reason, "status", "reason")
	_ = unstructured.SetNestedField(obj.Object, ro.UpdatedAt.Format(time.RFC3339), "status", "lastCheckedAt")
	_, err = dyn.Resource(rolloutGVR).Namespace(ns).UpdateStatus(ctx, obj, metav1.UpdateOptions{})
	if err != nil {
		return err
	}
	a.mu.Lock()
	a.rollouts[ro.ID] = ro
	a.mu.Unlock()
	return nil
}

func (a *App) waitingStatus(ctx context.Context, dyn dynamic.Interface, obj *unstructured.Unstructured, service string, weight int, phase, reason string) error {
	ro := &Rollout{ID: obj.GetNamespace() + "/" + obj.GetName(), Service: service, Scenario: "kubernetes", Phase: phase, Weight: weight, StartedAt: obj.GetCreationTimestamp().Time, UpdatedAt: time.Now().UTC(), Reason: reason}
	_ = unstructured.SetNestedField(obj.Object, phase, "status", "phase")
	_ = unstructured.SetNestedField(obj.Object, reason, "status", "reason")
	_ = unstructured.SetNestedField(obj.Object, ro.UpdatedAt.Format(time.RFC3339), "status", "lastCheckedAt")
	if _, err := dyn.Resource(rolloutGVR).Namespace(obj.GetNamespace()).UpdateStatus(ctx, obj, metav1.UpdateOptions{}); err != nil {
		return err
	}
	a.mu.Lock()
	a.rollouts[ro.ID] = ro
	a.mu.Unlock()
	return nil
}

func hasReadyEndpoints(ctx context.Context, client kubernetes.Interface, namespace, service string) (bool, error) {
	ep, err := client.CoreV1().Endpoints(namespace).Get(ctx, service, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	for _, subset := range ep.Subsets {
		if len(subset.Addresses) > 0 {
			return true, nil
		}
	}
	return false, nil
}

func (a *App) prometheusMetrics(ctx context.Context, service string) (map[string]float64, error) {
	base := env("PROMETHEUS_URL", "http://prometheus:9090")
	query := func(expr string) (float64, error) {
		u, err := url.Parse(base + "/api/v1/query")
		if err != nil {
			return 0, err
		}
		q := u.Query()
		q.Set("query", expr)
		u.RawQuery = q.Encode()
		reqCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, u.String(), nil)
		if err != nil {
			return 0, err
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return 0, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return 0, fmt.Errorf("prometheus returned %s", resp.Status)
		}
		var result struct {
			Status string `json:"status"`
			Data   struct {
				Result []struct {
					Value []any `json:"value"`
				} `json:"result"`
			} `json:"data"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			return 0, err
		}
		if result.Status != "success" || len(result.Data.Result) == 0 || len(result.Data.Result[0].Value) < 2 {
			return 0, nil
		}
		value, ok := result.Data.Result[0].Value[1].(string)
		if !ok {
			return 0, nil
		}
		return strconv.ParseFloat(value, 64)
	}
	label := strconv.Quote(service)
	total, err := query(fmt.Sprintf(`sum(rate(http_requests_total{service=%s}[1m]))`, label))
	if err != nil {
		return nil, err
	}
	bad, err := query(fmt.Sprintf(`sum(rate(http_requests_total{service=%s,status=~"5.."}[1m]))`, label))
	if err != nil {
		return nil, err
	}
	latency, err := query(fmt.Sprintf(`histogram_quantile(0.95, sum(rate(http_request_duration_seconds_bucket{service=%s}[1m])) by (le)) * 1000`, label))
	if err != nil {
		return nil, err
	}
	ratio := 0.0
	if total > 0 {
		ratio = bad / total
	}
	return map[string]float64{"error_rate": ratio, "latency_ms": latency, "request_rate": total}, nil
}

func setCanaryWeight(ctx context.Context, client kubernetes.Interface, ns, name string, weight int) error {
	annotations := map[string]string{"nginx.ingress.kubernetes.io/canary": "true", "nginx.ingress.kubernetes.io/canary-weight": strconv.Itoa(weight)}
	patch, err := json.Marshal(map[string]any{"metadata": map[string]any{"annotations": annotations}})
	if err != nil {
		return err
	}
	_, err = client.NetworkingV1().Ingresses(ns).Patch(ctx, name, types.MergePatchType, patch, metav1.PatchOptions{})
	return err
}
