package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteAvalancheTargetsBesideConfig(t *testing.T) {
	runDir := t.TempDir()
	if err := writeAvalancheTargets(runDir, metricScrapeLoadConfig{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(runDir, avalancheTargetsPath)); err != nil {
		t.Fatal(err)
	}
}
