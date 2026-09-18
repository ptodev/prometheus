package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestDiscoverTestsSkipsHiddenDirectories(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"enabled", ".disabled"} {
		dir := filepath.Join(root, name)
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "test.yml"), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	got, err := discoverTests(root)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{filepath.Join(root, "enabled")}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("discoverTests() = %v, want %v", got, want)
	}
}
