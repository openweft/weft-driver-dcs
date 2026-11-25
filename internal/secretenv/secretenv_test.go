package secretenv

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const sentinel = "s3cr3t-VALUE-do-not-echo"

func stub(t *testing.T, fn func(string) ([]byte, error)) {
	t.Helper()
	old := open
	open = func(_ context.Context, ref string) ([]byte, error) { return fn(ref) }
	t.Cleanup(func() { open = old; reset() })
	reset()
}

func TestPlaintextWorksWarnsAndIsUnset(t *testing.T) {
	stub(t, nil)
	t.Setenv("X_KEY", sentinel)
	var w bytes.Buffer
	if err := Resolve(context.Background(), &w, "X_KEY"); err != nil {
		t.Fatal(err)
	}
	if got := Get("X_KEY"); got != sentinel {
		t.Fatalf("Get = %q", got)
	}
	if _, set := os.LookupEnv("X_KEY"); set {
		t.Fatal("plaintext variable still in the environment")
	}
	if !strings.Contains(w.String(), "X_KEY_FROM") || strings.Contains(w.String(), sentinel) {
		t.Fatalf("warning = %q", w.String())
	}
}

func TestFromResolvesAndTrimsNewline(t *testing.T) {
	stub(t, func(ref string) ([]byte, error) {
		if ref != "file:/x" {
			t.Fatalf("ref = %q", ref)
		}
		return []byte(sentinel + "\n"), nil
	})
	t.Setenv("X_KEY_FROM", "file:/x")
	var w bytes.Buffer
	if err := Resolve(context.Background(), &w, "X_KEY"); err != nil {
		t.Fatal(err)
	}
	if Get("X_KEY") != sentinel {
		t.Fatalf("Get = %q", Get("X_KEY"))
	}
	if w.Len() != 0 {
		t.Fatalf("unexpected warning %q", w.String())
	}
}

func TestBothSetIsAnError(t *testing.T) {
	stub(t, nil)
	t.Setenv("X_KEY", sentinel)
	t.Setenv("X_KEY_FROM", "file:/x")
	err := Resolve(context.Background(), nil, "X_KEY")
	if err == nil || strings.Contains(err.Error(), sentinel) {
		t.Fatalf("err = %v", err)
	}
}

func TestUnresolvableRefNeverEchoesTheValue(t *testing.T) {
	stub(t, func(string) ([]byte, error) { return nil, errors.New("no such credential") })
	t.Setenv("X_KEY_FROM", "systemd-creds:"+sentinel)
	err := Resolve(context.Background(), nil, "X_KEY")
	if err == nil || !strings.Contains(err.Error(), "X_KEY_FROM") || !strings.Contains(err.Error(), "no such credential") {
		t.Fatalf("err = %v", err)
	}
}

func TestRealSecretsourceFileAndMissingFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("secretsource file: refuses a temp file whose inherited DACL grants a non-owner")
	}
	reset()
	t.Cleanup(reset)
	dir := t.TempDir()
	p := filepath.Join(dir, "key")
	if err := os.WriteFile(p, []byte(sentinel+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("X_KEY_FROM", "file:"+p)
	if err := Resolve(context.Background(), nil, "X_KEY"); err != nil {
		t.Fatal(err)
	}
	if Get("X_KEY") != sentinel {
		t.Fatalf("Get = %q", Get("X_KEY"))
	}
	t.Setenv("Z_KEY_FROM", "file:"+filepath.Join(dir, "absent"))
	err := Resolve(context.Background(), nil, "Z_KEY")
	if err == nil || !strings.Contains(err.Error(), "Z_KEY_FROM") || errors.Is(err, errEmpty) {
		t.Fatalf("err = %v", err)
	}
	if Get("Z_KEY") != "" {
		t.Fatal("a failed reference stored a value")
	}
}

func TestEmptySecretIsAnError(t *testing.T) {
	stub(t, func(string) ([]byte, error) { return []byte("\n"), nil })
	t.Setenv("X_KEY_FROM", "file:/x")
	if err := Resolve(context.Background(), nil, "X_KEY"); err == nil {
		t.Fatal("empty secret accepted")
	}
}

func TestUnsetStaysEmptyAndGetFallsBackToEnv(t *testing.T) {
	stub(t, nil)
	if err := Resolve(context.Background(), nil, "X_KEY"); err != nil {
		t.Fatal(err)
	}
	if Get("X_KEY") != "" {
		t.Fatal("expected empty")
	}
	t.Setenv("Y_KEY", "from-env")
	if Get("Y_KEY") != "from-env" {
		t.Fatal("no env fallback")
	}
}
