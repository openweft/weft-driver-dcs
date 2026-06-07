// vrm_client_test.go — exercises the VRM REST client against an
// httptest.Server simulating the relevant FusionCompute endpoints.
// Covers : login + token caching, 401 → refresh, task polling
// (success + failure), 404 → isNotFound.

package builtin

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func quietLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestVRMClient_LoginThenAuthenticatedCall(t *testing.T) {
	var loginCalls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/service/session":
			atomic.AddInt32(&loginCalls, 1)
			w.Header().Set("X-Auth-Token", "tok-123")
			w.WriteHeader(http.StatusOK)
		case "/service/tasks/t-1":
			if r.Header.Get("X-Auth-Token") != "tok-123" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			json.NewEncoder(w).Encode(vrmTaskStatus{Status: "success", Progress: 100})
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer srv.Close()

	c := newVRMClient(srv.URL, "admin", "password", true, quietLog())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.waitTask(ctx, vrmTask{TaskUUID: "t-1"}); err != nil {
		t.Fatalf("waitTask: %v", err)
	}
	if got := atomic.LoadInt32(&loginCalls); got != 1 {
		t.Errorf("login calls = %d, want 1", got)
	}

	// Second call must reuse the cached token.
	if err := c.waitTask(ctx, vrmTask{TaskUUID: "t-1"}); err != nil {
		t.Fatalf("waitTask #2: %v", err)
	}
	if got := atomic.LoadInt32(&loginCalls); got != 1 {
		t.Errorf("login re-issued on cached token : %d", got)
	}
}

func TestVRMClient_401TriggersRefresh(t *testing.T) {
	var loginCalls int32
	var taskCalls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/service/session":
			atomic.AddInt32(&loginCalls, 1)
			w.Header().Set("X-Auth-Token", "tok-fresh")
			w.WriteHeader(http.StatusOK)
		case "/service/tasks/t-1":
			n := atomic.AddInt32(&taskCalls, 1)
			if n == 1 {
				// First request with the prefilled stale token : 401.
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			if r.Header.Get("X-Auth-Token") != "tok-fresh" {
				t.Errorf("retry did not use refreshed token : %s", r.Header.Get("X-Auth-Token"))
			}
			json.NewEncoder(w).Encode(vrmTaskStatus{Status: "success"})
		}
	}))
	defer srv.Close()

	c := newVRMClient(srv.URL, "admin", "p", true, quietLog())
	c.mu.Lock()
	c.token = "tok-stale" // simulate a cached expired token
	c.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.waitTask(ctx, vrmTask{TaskUUID: "t-1"}); err != nil {
		t.Fatalf("waitTask: %v", err)
	}
	if got := atomic.LoadInt32(&loginCalls); got != 1 {
		t.Errorf("login calls after 401 = %d, want 1", got)
	}
}

func TestVRMClient_TaskFailureSurfacesReason(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/service/session":
			w.Header().Set("X-Auth-Token", "tok")
			w.WriteHeader(http.StatusOK)
		case "/service/tasks/t-bad":
			json.NewEncoder(w).Encode(vrmTaskStatus{Status: "failed", Reason: "datastore full"})
		}
	}))
	defer srv.Close()

	c := newVRMClient(srv.URL, "admin", "p", true, quietLog())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := c.waitTask(ctx, vrmTask{TaskUUID: "t-bad"})
	if err == nil {
		t.Fatal("expected error from failed task")
	}
	if !strings.Contains(err.Error(), "datastore full") {
		t.Errorf("reason not surfaced : %v", err)
	}
}

func TestVRMClient_NotFoundCollapsesToErr(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/service/session" {
			w.Header().Set("X-Auth-Token", "tok")
			w.WriteHeader(http.StatusOK)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	c := newVRMClient(srv.URL, "admin", "p", true, quietLog())
	ctx := context.Background()
	err := c.do(ctx, http.MethodDelete, "/service/sites/x/vms/missing?isFormat=true", nil, nil)
	if !isNotFound(err) {
		t.Errorf("isNotFound did not match : %v", err)
	}
}

func TestIsNotFound_StringMatches(t *testing.T) {
	cases := []struct {
		err  string
		want bool
	}{
		{"vrm DELETE /x: status 404, body 404 page not found", true},
		{"some message including vmNotFound here", true},
		{"random reason: not found in db", true},
		{"status 500 internal", false},
		{"", false},
	}
	for _, c := range cases {
		got := isNotFound(errorString(c.err))
		if got != c.want {
			t.Errorf("isNotFound(%q) = %v, want %v", c.err, got, c.want)
		}
	}
}

func TestIsAlreadyExists_StringMatches(t *testing.T) {
	if !isAlreadyExists(errorString("vrm POST: status 409, body duplicate uuid")) {
		t.Error("409 not detected")
	}
	if isAlreadyExists(errorString("vrm POST: status 500")) {
		t.Error("500 falsely matched")
	}
}

// errorString is a tiny test helper that turns a string into an error
// (avoids pulling in a separate stub-error type for two tests).
type errorString string

func (e errorString) Error() string { return string(e) }
