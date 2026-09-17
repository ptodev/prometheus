package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

const (
	avalanchePort = 9001

	avalancheTargetsPath = "avalanche_targets.json"
)

const seriesPerTarget = 10000

func (a metricScrapeLoadConfig) targetCount() int {
	return max(1, (a.ActiveSeries+seriesPerTarget-1)/seriesPerTarget)
}

func (a metricScrapeLoadConfig) totalSeries() int {
	return seriesPerTarget * a.targetCount()
}

func (a metricScrapeLoadConfig) samplesPerSecond() float64 {
	interval, err := time.ParseDuration(a.ScrapeInterval)
	if err != nil || interval <= 0 {
		return 0
	}
	return float64(a.totalSeries()) / interval.Seconds()
}

const avalancheSeriesPerInstance = 1_000_000

func (a metricScrapeLoadConfig) instanceCount() int {
	n := (a.totalSeries() + avalancheSeriesPerInstance - 1) / avalancheSeriesPerInstance
	return min(max(1, n), a.targetCount())
}

func (a metricScrapeLoadConfig) args(port int) []string {
	return []string{
		fmt.Sprintf("--port=%d", port),
		"--gauge-metric-count=300",
		"--counter-metric-count=300",
		"--histogram-metric-count=100",
		"--histogram-metric-bucket-count=7",
		"--native-histogram-metric-count=300",
		"--summary-metric-count=0",
		"--series-count=4",
		"--value-interval=30",
		"--series-interval=0",
		"--metric-interval=0",
	}
}

func avalanchePorts(av metricScrapeLoadConfig) []int {
	ports := make([]int, av.instanceCount())
	for i := range ports {
		ports[i] = avalanchePort + i
	}
	return ports
}

func startAvalanches(ctx context.Context, runDir string, av metricScrapeLoadConfig) ([]*exec.Cmd, error) {
	var started []*exec.Cmd
	for _, port := range avalanchePorts(av) {
		cmd := exec.CommandContext(ctx, "avalanche", av.args(port)...)
		logF, err := os.Create(filepath.Join(runDir, fmt.Sprintf("avalanche-%d.log", port)))
		if err != nil {
			return started, err
		}
		cmd.Stdout, cmd.Stderr = logF, logF
		cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
		if err := cmd.Start(); err != nil {
			logF.Close()
			return started, fmt.Errorf("port %d: is avalanche installed? (go install github.com/prometheus-community/avalanche/cmd/avalanche@latest): %w", port, err)
		}
		logF.Close()
		started = append(started, cmd)
	}
	return started, nil
}

func stopAvalanches(cmds []*exec.Cmd) {
	for _, cmd := range cmds {
		if cmd.Process == nil {
			continue
		}
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}
}

func writeAvalancheTargets(runDir string, av metricScrapeLoadConfig) error {
	ports := avalanchePorts(av)
	n := av.targetCount()

	var b bytes.Buffer
	b.WriteByte('[')
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"targets":["127.0.0.1:%d"],"labels":{"instance":"avalanche-%06d"}}`,
			ports[i%len(ports)], i)
	}
	b.WriteByte(']')

	return os.WriteFile(filepath.Join(runDir, avalancheTargetsPath), b.Bytes(), 0o644)
}
