// Package secretenv lets a secret that used to be an environment variable be
// given by reference instead.
//
// For each secret NAME the operator may set NAME (plaintext, deprecated: it is
// readable in /proc/PID/environ and inherited by every child process) or
// NAME_FROM, a github.com/go-authn/secretsource reference (systemd-creds:,
// keychain:, keepassxc:, file:). Both set is an error. [Resolve] reads the
// value once, stores it in memory and unsets the plaintext variable so that
// child processes do not inherit it; [Get] returns the stored value.
package secretenv

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"github.com/go-authn/secretsource"
)

// open is replaced by tests.
var open = func(ctx context.Context, ref string) ([]byte, error) {
	s, err := secretsource.Open(ctx, ref)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	return append([]byte(nil), s.Bytes()...), nil
}

var (
	mu     sync.RWMutex
	values = map[string]string{}
)

// Resolve reads every named secret. warn receives the deprecation notice for
// a plaintext variable (nil discards). Errors never contain the value.
func Resolve(ctx context.Context, warn io.Writer, names ...string) error {
	if warn == nil {
		warn = io.Discard
	}
	for _, name := range names {
		plain, plainSet := os.LookupEnv(name)
		ref := os.Getenv(name + "_FROM")
		switch {
		case ref != "" && plainSet && plain != "":
			return fmt.Errorf("%s and %s_FROM are both set; use only %s_FROM", name, name, name)
		case ref != "":
			raw, err := open(ctx, ref)
			if err != nil {
				return fmt.Errorf("%s_FROM: %w", name, err)
			}
			v := strings.TrimRight(string(raw), "\r\n")
			clear(raw)
			if v == "" {
				return fmt.Errorf("%s_FROM: %w", name, errEmpty)
			}
			store(name, v)
		case plain != "":
			fmt.Fprintf(warn, "%s is deprecated: a plaintext environment variable is readable in /proc/PID/environ and inherited by child processes; use %s_FROM=<ref> (systemd-creds:, keychain:, keepassxc:, file:)\n", name, name)
			store(name, plain)
		}
		_ = os.Unsetenv(name)
	}
	return nil
}

var errEmpty = errors.New("the secret is empty")

func store(name, v string) {
	mu.Lock()
	values[name] = v
	mu.Unlock()
}

// Get returns the secret resolved by [Resolve]. A name that was never
// resolved falls back to the process environment, so code that runs without
// the startup step (tests, embedding) keeps working.
func Get(name string) string {
	mu.RLock()
	v, ok := values[name]
	mu.RUnlock()
	if ok {
		return v
	}
	return os.Getenv(name)
}

// reset forgets every stored secret (tests).
func reset() {
	mu.Lock()
	clear(values)
	mu.Unlock()
}
