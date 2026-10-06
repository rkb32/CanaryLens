package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestTokenAuthRequiresBearerToken(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	handler := tokenAuth(next, "0123456789abcdef0123456789abcdef")

	for _, tc := range []struct {
		name, authorization string
		path                string
		want                int
	}{
		{name: "missing", path: "/api/rollouts", want: http.StatusUnauthorized},
		{name: "wrong scheme", path: "/api/rollouts", authorization: "Basic abc", want: http.StatusUnauthorized},
		{name: "wrong token", path: "/api/rollouts", authorization: "Bearer wrong", want: http.StatusUnauthorized},
		{name: "valid token", path: "/api/rollouts", authorization: "Bearer 0123456789abcdef0123456789abcdef", want: http.StatusNoContent},
		{name: "health check is public", path: "/healthz", want: http.StatusNoContent},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			req.Header.Set("Authorization", tc.authorization)
			res := httptest.NewRecorder()
			handler.ServeHTTP(res, req)
			if res.Code != tc.want {
				t.Fatalf("got status %d, want %d", res.Code, tc.want)
			}
		})
	}
}
