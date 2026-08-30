package app

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveWorkspaceFileAcceptsRelativeAndAbsoluteChildren(t *testing.T) {
	root := t.TempDir()
	child := filepath.Join(root, "src", "main.go")
	if err := os.MkdirAll(filepath.Dir(child), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(child, []byte("package main"), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, input := range []string{filepath.Join("src", "main.go"), child} {
		resolved, err := resolveWorkspaceFile(root, input)
		if err != nil {
			t.Fatalf("resolve %q: %v", input, err)
		}
		if resolved != child {
			t.Fatalf("resolve %q = %q, want %q", input, resolved, child)
		}
	}
}

func TestResolveWorkspaceFileRejectsOutsideAndDirectory(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "workspace")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(base, "outside.txt")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := resolveWorkspaceFile(root, outside); err == nil {
		t.Fatal("outside file was accepted")
	}
	if _, err := resolveWorkspaceFile(root, root); err == nil {
		t.Fatal("workspace directory was accepted as a file")
	}
}
