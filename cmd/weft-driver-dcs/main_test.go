package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const (
	sentinel    = "s3cr3t-VALUE-do-not-echo"
	envPassword = "WEFT_DCS_PASSWORD"
)

func TestPlainPasswordWorksWarnsAndIsUnset(t *testing.T) {
	t.Setenv(envPassword, sentinel)
	var w bytes.Buffer
	got, err := resolvePassword(context.Background(), &w)
	if err != nil || got != sentinel {
		t.Fatalf("got %q, %v", got, err)
	}
	if !strings.Contains(w.String(), envPassword+"_FROM") || strings.Contains(w.String(), sentinel) {
		t.Fatalf("warning = %q", w.String())
	}
	if _, set := os.LookupEnv(envPassword); set {
		t.Fatal("plaintext variable still in the environment (children would inherit it)")
	}
}

func TestPasswordFromFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("secretsource file: refuses a temp file whose inherited DACL grants a non-owner")
	}
	p := filepath.Join(t.TempDir(), "pw")
	if err := os.WriteFile(p, []byte(sentinel+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(envPassword+"_FROM", "file:"+p)
	var w bytes.Buffer
	got, err := resolvePassword(context.Background(), &w)
	if err != nil || got != sentinel {
		t.Fatalf("got %q, %v", got, err)
	}
	if w.Len() != 0 {
		t.Fatalf("unexpected warning %q", w.String())
	}
}

func TestBothSetIsRefusedWithoutTheValue(t *testing.T) {
	t.Setenv(envPassword, sentinel)
	t.Setenv(envPassword+"_FROM", "file:/x")
	_, err := resolvePassword(context.Background(), nil)
	if err == nil || strings.Contains(err.Error(), sentinel) {
		t.Fatalf("err = %v", err)
	}
}

func TestMissingRefAndEmptyValueAreErrors(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("secretsource file: path semantics differ on Windows")
	}
	dir := t.TempDir()
	t.Setenv(envPassword+"_FROM", "file:"+filepath.Join(dir, "absent"))
	if _, err := resolvePassword(context.Background(), nil); err == nil || !strings.Contains(err.Error(), envPassword+"_FROM") {
		t.Fatalf("missing ref: err = %v", err)
	}
	empty := filepath.Join(dir, "empty")
	if err := os.WriteFile(empty, []byte("\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(envPassword+"_FROM", "file:"+empty)
	if _, err := resolvePassword(context.Background(), nil); err == nil {
		t.Fatal("empty secret accepted")
	}
}
