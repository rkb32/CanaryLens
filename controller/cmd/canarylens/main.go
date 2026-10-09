package main

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/encoding"
)

type jsonCodec struct{}

func (jsonCodec) Name() string                    { return "json" }
func (jsonCodec) Marshal(v any) ([]byte, error)   { return json.Marshal(v) }
func (jsonCodec) Unmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }

type Rollout struct {
	ID              string    `json:"id"`
	Service         string    `json:"service"`
	Scenario        string    `json:"scenario"`
	Phase           string    `json:"phase"`
	Weight          int       `json:"weight"`
	ErrorRate       float64   `json:"errorRate"`
	Score           float64   `json:"score"`
	Reason          string    `json:"reason"`
	StableLatencyMs float64   `json:"stableLatencyMs"`
	CanaryLatencyMs float64   `json:"canaryLatencyMs"`
	StartedAt       time.Time `json:"startedAt"`
	UpdatedAt       time.Time `json:"updatedAt"`
}
type Event struct {
	ID        int64     `json:"id"`
	RolloutID string    `json:"rolloutId"`
	At        time.Time `json:"at"`
	Kind      string    `json:"kind"`
	Message   string    `json:"message"`
	Weight    int       `json:"weight"`
	ErrorRate float64   `json:"errorRate"`
}
type App struct {
	mu        sync.RWMutex
	eventMu   sync.RWMutex
	rollouts  map[string]*Rollout
	events    []Event
	nextEvent int64
	db        *sql.DB
	grpc      *grpc.ClientConn
	steps     []int
	threshold float64
	interval  time.Duration
}

func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
func main() {
	encoding.RegisterCodec(jsonCodec{})
	ctx := context.Background()
	mode := env("MODE", "demo")
	var db *sql.DB
	var err error
	if databaseURL := os.Getenv("DATABASE_URL"); databaseURL != "" {
		db, err = sql.Open("pgx", databaseURL)
		if err != nil {
			log.Fatal(err)
		}
		for i := 0; i < 30; i++ {
			if err = db.PingContext(ctx); err == nil {
				break
			}
			time.Sleep(time.Second)
		}
		if err != nil {
			log.Fatalf("postgres unavailable: %v", err)
		}
		_, err = db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS events (id BIGSERIAL PRIMARY KEY, rollout_id TEXT NOT NULL, at TIMESTAMPTZ NOT NULL, kind TEXT NOT NULL, message TEXT NOT NULL, weight INT NOT NULL, error_rate DOUBLE PRECISION NOT NULL)`)
		if err != nil {
			log.Fatal(err)
		}
	} else if mode == "kubernetes" {
		log.Fatal("DATABASE_URL is required in kubernetes mode")
	} else {
		log.Print("DATABASE_URL unset; demo event history is in memory only")
	}
	conn, err := grpc.NewClient(env("SCORER_ADDR", "localhost:50051"), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatal(err)
	}
	steps := []int{5, 25, 50, 100}
	if raw := os.Getenv("ROLLOUT_STEPS"); raw != "" {
		steps = nil
		for _, p := range strings.Split(raw, ",") {
			n, e := strconv.Atoi(strings.TrimSpace(p))
			if e == nil && n > 0 && n <= 100 {
				steps = append(steps, n)
			}
		}
	}
	if len(steps) == 0 {
		log.Fatal("ROLLOUT_STEPS must contain integers from 1 to 100")
	}
	interval, err := time.ParseDuration(env("CHECK_INTERVAL", "15s"))
	if err != nil || interval <= 0 {
		log.Fatal("CHECK_INTERVAL must be a positive duration")
	}
	threshold, err := strconv.ParseFloat(env("ERROR_THRESHOLD", "0.01"), 64)
	if err != nil || threshold < 0 {
		log.Fatal("ERROR_THRESHOLD must be non-negative")
	}
	apiToken := os.Getenv("API_TOKEN")
	if mode == "kubernetes" && len(apiToken) < 32 {
		log.Fatal("API_TOKEN must contain at least 32 characters in kubernetes mode")
	}
	a := &App{rollouts: map[string]*Rollout{}, db: db, grpc: conn, steps: steps, threshold: threshold, interval: interval}
	if env("DEMO_MODE", "false") == "true" {
		go a.demoLoop(ctx)
	}
	if mode == "kubernetes" {
		go a.watchKubernetes(ctx)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200); _, _ = w.Write([]byte("ok")) })
	mux.HandleFunc("GET /api/rollouts", a.listRollouts)
	mux.HandleFunc("GET /api/events", a.listEvents)
	mux.HandleFunc("GET /api/config", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]bool{"demoMode": env("DEMO_MODE", "false") == "true"})
	})
	mux.HandleFunc("POST /api/demo/start", a.startDemo)
	port := env("PORT", "8080")
	log.Printf("CanaryLens API listening on :%s", port)
	log.Fatal(http.ListenAndServe(":"+port, cors(tokenAuth(mux, apiToken))))
}
func tokenAuth(next http.Handler, token string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if token == "" || r.URL.Path == "/healthz" {
			next.ServeHTTP(w, r)
			return
		}
		header := r.Header.Get("Authorization")
		if !strings.HasPrefix(header, "Bearer ") {
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		provided := strings.TrimPrefix(header, "Bearer ")
		// Compare fixed-size digests so the comparison does not leak the token's length.
		providedHash := sha256.Sum256([]byte(provided))
		tokenHash := sha256.Sum256([]byte(token))
		if subtle.ConstantTimeCompare(providedHash[:], tokenHash[:]) != 1 {
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}
func cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		allowed := false
		for _, candidate := range strings.Split(env("CORS_ORIGINS", "http://localhost:3000"), ",") {
			if strings.TrimSpace(candidate) == origin && origin != "" {
				allowed = true
				break
			}
		}
		w.Header().Add("Vary", "Origin")
		if allowed {
			w.Header().Set("Access-Control-Allow-Origin", origin)
		}
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		w.Header().Set("Access-Control-Allow-Methods", "GET,POST,OPTIONS")
		if r.Method == "OPTIONS" {
			if !allowed && origin != "" {
				http.Error(w, "origin not allowed", http.StatusForbidden)
				return
			}
			return
		}
		next.ServeHTTP(w, r)
	})
}
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
func (a *App) listRollouts(w http.ResponseWriter, r *http.Request) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	out := make([]*Rollout, 0, len(a.rollouts))
	for _, v := range a.rollouts {
		c := *v
		out = append(out, &c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	writeJSON(w, out)
}
func (a *App) listEvents(w http.ResponseWriter, r *http.Request) {
	if a.db == nil {
		a.eventMu.RLock()
		defer a.eventMu.RUnlock()
		start := len(a.events) - 100
		if start < 0 {
			start = 0
		}
		out := make([]Event, 0, len(a.events)-start)
		for i := len(a.events) - 1; i >= start; i-- {
			out = append(out, a.events[i])
		}
		writeJSON(w, out)
		return
	}
	rows, err := a.db.QueryContext(r.Context(), `SELECT id,rollout_id,at,kind,message,weight,error_rate FROM events ORDER BY id DESC LIMIT 100`)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	defer rows.Close()
	out := []Event{}
	for rows.Next() {
		var e Event
		if err = rows.Scan(&e.ID, &e.RolloutID, &e.At, &e.Kind, &e.Message, &e.Weight, &e.ErrorRate); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		out = append(out, e)
	}
	writeJSON(w, out)
}
func (a *App) startDemo(w http.ResponseWriter, r *http.Request) {
	if env("DEMO_MODE", "false") != "true" {
		http.Error(w, "demo mode is disabled", 404)
		return
	}
	var req struct {
		Scenario string `json:"scenario"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON", 400)
		return
	}
	if req.Scenario != "bad" && req.Scenario != "healthy" {
		http.Error(w, "scenario must be bad or healthy", 400)
		return
	}
	now := time.Now().UTC()
	id := fmt.Sprintf("rollout-%d", now.UnixNano())
	initialWeight := a.steps[0]
	ro := &Rollout{ID: id, Service: "checkout-api", Scenario: req.Scenario, Phase: "Progressing", Weight: initialWeight, StartedAt: now, UpdatedAt: now, Reason: "Initial canary traffic enabled; waiting for first metrics check"}
	a.mu.Lock()
	a.rollouts[id] = ro
	a.mu.Unlock()
	a.event(r.Context(), ro, "started", "Canary rollout started", 0, 0)
	a.event(r.Context(), ro, "progressed", fmt.Sprintf("Initial canary traffic set to %d%%", initialWeight), initialWeight, 0)
	writeJSON(w, ro)
}
func (a *App) event(ctx context.Context, ro *Rollout, kind, msg string, weight int, rate float64) {
	at := time.Now().UTC()
	if a.db == nil {
		a.eventMu.Lock()
		a.nextEvent++
		a.events = append(a.events, Event{ID: a.nextEvent, RolloutID: ro.ID, At: at, Kind: kind, Message: msg, Weight: weight, ErrorRate: rate})
		if len(a.events) > 1000 {
			a.events = append([]Event(nil), a.events[len(a.events)-1000:]...)
		}
		a.eventMu.Unlock()
		return
	}
	_, err := a.db.ExecContext(ctx, `INSERT INTO events(rollout_id,at,kind,message,weight,error_rate) VALUES($1,$2,$3,$4,$5,$6)`, ro.ID, at, kind, msg, weight, rate)
	if err != nil {
		log.Printf("save event: %v", err)
	}
}
func (a *App) score(ctx context.Context, stable, canary map[string]float64) (float64, string, error) {
	req := map[string]any{"stable": stable, "canary": canary}
	var res struct {
		Score  float64 `json:"score"`
		Reason string  `json:"reason"`
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	err := a.grpc.Invoke(ctx, "/canarylens.Scorer/Score", req, &res, grpc.ForceCodec(jsonCodec{}))
	return res.Score, res.Reason, err
}
func (a *App) demoLoop(ctx context.Context) {
	ticker := time.NewTicker(a.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.tick(ctx)
		}
	}
}
func (a *App) tick(ctx context.Context) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, ro := range a.rollouts {
		if ro.Phase != "Progressing" {
			continue
		}
		rate := 0.002
		if ro.Scenario == "bad" && ro.Weight >= 5 {
			rate = 0.035
		}
		ro.ErrorRate = rate
		ro.StableLatencyMs = 115
		ro.CanaryLatencyMs = 130
		ro.UpdatedAt = time.Now().UTC()
		a.event(ctx, ro, "sample", fmt.Sprintf("Metric check: %.2f%% 5xx, stable/canary latency %.0f/%.0f ms", rate*100, ro.StableLatencyMs, ro.CanaryLatencyMs), ro.Weight, rate)
		stable := map[string]float64{"error_rate": 0.002, "latency_ms": ro.StableLatencyMs}
		canary := map[string]float64{"error_rate": rate, "latency_ms": ro.CanaryLatencyMs}
		score, reason, err := a.score(ctx, stable, canary)
		if err == nil {
			ro.Score = score
			ro.Reason = reason
		} else {
			ro.Reason = "Scoring service unavailable; metric threshold remains authoritative"
		}
		decision := evaluateRollout(rate, a.threshold, ro.Weight, a.steps)
		if decision.Rollback {
			ro.Phase = "RolledBack"
			ro.Weight = 0
			msg := fmt.Sprintf("Canary error rate %.2f%% exceeded %.2f%%; traffic returned to stable", rate*100, a.threshold*100)
			ro.Reason = msg
			a.event(ctx, ro, "rollback", msg, 0, rate)
			continue
		}
		if decision.Complete {
			ro.Phase = "Succeeded"
			ro.Weight = 100
			ro.UpdatedAt = time.Now().UTC()
			a.event(ctx, ro, "completed", "All rollout steps passed; canary promoted", 100, rate)
			continue
		}
		ro.Weight = decision.Weight
		ro.UpdatedAt = time.Now().UTC()
		msg := fmt.Sprintf("Metrics healthy at %.2f%% errors; advanced canary traffic to %d%%", rate*100, ro.Weight)
		a.event(ctx, ro, "progressed", msg, ro.Weight, rate)
	}
}
