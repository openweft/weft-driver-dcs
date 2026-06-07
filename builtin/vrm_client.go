// vrm_client.go — REST client for the Huawei FusionCompute VRM
// (Virtualization Resource Manager). The protocol is documented in
// the "FusionCompute V100R006C00 ToB API Reference" (publicly
// downloadable from Huawei support).
//
// Authentication : cookie-based via POST /service/session. The VRM
// returns an `X-Auth-Token` header that's required on every subsequent
// call ; tokens expire (default 1 h TTL) and we refresh transparently
// on 401.
//
// Concurrency : one *vrmClient is safe for concurrent use ; the token
// mutex serialises the rare refresh path so a 401 storm doesn't
// authenticate 50 sessions at once.

package builtin

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

// vrmClient wraps the FusionCompute REST API. Construct once per
// driver instance ; reused across HypervisorDriver / VolumeDriver /
// NetworkDriver calls.
type vrmClient struct {
	endpoint   string // VRM base URL, no trailing /
	username   string
	password   string
	httpClient *http.Client
	log        *slog.Logger

	mu    sync.Mutex
	token string
}

// newVRMClient builds a REST client against the VRM. The first
// request triggers login ; subsequent requests reuse the cached
// token until a 401 forces refresh.
func newVRMClient(endpoint, username, password string, insecure bool, log *slog.Logger) *vrmClient {
	endpoint = strings.TrimRight(endpoint, "/")
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: insecure}, // #nosec G402 — operator opt-in via Insecure flag
		// Pool sizing for the typical 5-VM cluster — bigger ones
		// scale linearly with the host pool.
		MaxIdleConns:    16,
		IdleConnTimeout: 90 * time.Second,
	}
	return &vrmClient{
		endpoint:   endpoint,
		username:   username,
		password:   password,
		httpClient: &http.Client{Transport: transport, Timeout: 30 * time.Second},
		log:        log,
	}
}

// login posts the credentials and stores the token. FusionCompute
// expects a SHA-256-hashed password ; the raw operator-supplied
// password is hashed once at login time.
func (c *vrmClient) login(ctx context.Context) error {
	hashed := sha256.Sum256([]byte(c.password))
	body, err := json.Marshal(map[string]any{
		"userName": c.username,
		"password": hex.EncodeToString(hashed[:]),
	})
	if err != nil {
		return fmt.Errorf("encode login: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint+"/service/session", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("new login request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Auth-User", c.username) // VRM puts the user in two places
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("vrm login: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("vrm login: status %d, body %s", resp.StatusCode, string(b))
	}
	token := resp.Header.Get("X-Auth-Token")
	if token == "" {
		// Some VRM versions echo it in the JSON body under
		// "authToken". Decode defensively.
		var jb struct {
			AuthToken string `json:"authToken"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&jb)
		token = jb.AuthToken
	}
	if token == "" {
		return errors.New("vrm login: empty X-Auth-Token")
	}
	c.mu.Lock()
	c.token = token
	c.mu.Unlock()
	c.log.Info("vrm login succeeded", "endpoint", c.endpoint)
	return nil
}

// do executes a REST call. Auto-refreshes the token on 401. The
// out pointer (if non-nil) receives the JSON-decoded response body.
//
// Up to 2 HTTP request attempts after any required logins : the first
// covers the steady-state cached-token path ; the second handles a
// just-expired token (401 → clear → re-login → re-request).
func (c *vrmClient) do(ctx context.Context, method, path string, in, out any) error {
	for attempt := 0; attempt < 2; attempt++ {
		c.mu.Lock()
		token := c.token
		c.mu.Unlock()
		if token == "" {
			if err := c.login(ctx); err != nil {
				return err
			}
			c.mu.Lock()
			token = c.token
			c.mu.Unlock()
		}
		var body io.Reader
		if in != nil {
			b, err := json.Marshal(in)
			if err != nil {
				return fmt.Errorf("encode body: %w", err)
			}
			body = bytes.NewReader(b)
		}
		req, err := http.NewRequestWithContext(ctx, method, c.endpoint+path, body)
		if err != nil {
			return fmt.Errorf("new request: %w", err)
		}
		req.Header.Set("Accept", "application/json")
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		req.Header.Set("X-Auth-Token", token)
		resp, err := c.httpClient.Do(req)
		if err != nil {
			return fmt.Errorf("vrm %s %s: %w", method, path, err)
		}
		// 401 => token expired ; clear + retry once via the for loop.
		if resp.StatusCode == http.StatusUnauthorized {
			resp.Body.Close()
			c.mu.Lock()
			c.token = ""
			c.mu.Unlock()
			continue
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 400 {
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			return fmt.Errorf("vrm %s %s: status %d, body %s", method, path, resp.StatusCode, string(b))
		}
		if out != nil && resp.StatusCode != http.StatusNoContent {
			if err := json.NewDecoder(resp.Body).Decode(out); err != nil && !errors.Is(err, io.EOF) {
				return fmt.Errorf("decode response: %w", err)
			}
		}
		return nil
	}
	return errors.New("vrm: gave up after token refresh retry")
}

// vrmTask is the envelope FusionCompute returns from every mutating
// call. The actual mutation is async ; the task UUID is what we poll.
type vrmTask struct {
	TaskURI    string `json:"taskUri"`
	TaskUUID   string `json:"taskUuid"`
	Reason     string `json:"reason,omitempty"`
}

// vrmTaskStatus is what GET /service/tasks/{uuid} returns.
type vrmTaskStatus struct {
	Status   string `json:"status"`   // "running" | "success" | "failed"
	Progress int    `json:"progress"` // 0..100
	Reason   string `json:"reason,omitempty"`
}

// waitTask polls a task to completion. Returns the final status or
// errors when ctx expires.
func (c *vrmClient) waitTask(ctx context.Context, task vrmTask) error {
	if task.TaskUUID == "" {
		return errors.New("vrm waitTask: empty task uuid")
	}
	path := task.TaskURI
	if path == "" {
		path = "/service/tasks/" + task.TaskUUID
	}
	deadline, hasDL := ctx.Deadline()
	if !hasDL {
		// FusionCompute mutations rarely take more than 2 min ;
		// the agent's per-call ctx usually carries a tighter one.
		deadline = time.Now().Add(2 * time.Minute)
	}
	for time.Now().Before(deadline) {
		var st vrmTaskStatus
		if err := c.do(ctx, http.MethodGet, path, nil, &st); err != nil {
			return fmt.Errorf("poll task %s: %w", task.TaskUUID, err)
		}
		switch st.Status {
		case "success", "completed", "finished":
			return nil
		case "failed", "error":
			return fmt.Errorf("vrm task %s failed: %s", task.TaskUUID, st.Reason)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return fmt.Errorf("vrm task %s did not complete within deadline", task.TaskUUID)
}
