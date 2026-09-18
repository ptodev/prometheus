package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadTestSubject(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "test.yml"), []byte("subject_under_test:\n  binary_path: /tmp/custom-prometheus\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	config := "scrape_configs:\n- job_name: avalanche\nremote_write:\n- url: http://" + alloyReceiveAddr + "/api/v1/write\n"
	if err := os.WriteFile(filepath.Join(dir, "prometheus.yml"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}

	got, _, _, err := loadTest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.Subject.BinaryPath != "/tmp/custom-prometheus" {
		t.Fatalf("binary path = %q", got.Subject.BinaryPath)
	}
}
