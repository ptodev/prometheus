// This file holds everything specific to generating the metric-scrape load:
// metricScrapeLoadConfig's methods and the avalanche process lifecycle. The
// type itself and how it's decoded from common.yml live in config.go.
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
	// avalanchePort is where the first avalanche instance listens;
	// consecutive instances take the next ports up (see avalanchePorts).
	avalanchePort = 9001

	// avalancheTargetsPath is where the file_sd file listing every avalanche
	// scrape target lives -- a fixed name, not one per run, because every
	// checked-in test prometheus.yml references it by this literal relative
	// path (resolved against bench's own working directory, which is also
	// where this gets written; see writeAvalancheTargets). Fixed rather than
	// per-label is safe because the whole system already assumes one test
	// runs at a time -- avalanche's own ports are fixed too.
	avalancheTargetsPath = "avalanche_targets.json"
)

// seriesPerTarget is how many series one avalanche instance exposes, and so
// how many series each scraped target contributes. Fixed rather than
// configurable: a realistic ~10,000-series target, mostly gauges and
// counters with some histograms, roughly what a heavy exporter looks like
// (kube-state-metrics, cadvisor, a big node_exporter). See args for the
// avalanche flags this corresponds to.
//
//	4 x (300 gauge + 300 counter + 100 histogram x10 + 300 native_histogram x3) = 10,000
const seriesPerTarget = 10000

// targetCount is how many logical scrape targets are needed to reach
// ActiveSeries. Each is the same avalanche endpoint under a different
// `instance` label, which Prometheus treats as a separate target because
// target identity is the whole label set.
func (a metricScrapeLoadConfig) targetCount() int {
	if a.ActiveSeries <= 0 {
		return 1
	}
	n := a.ActiveSeries / seriesPerTarget
	if a.ActiveSeries%seriesPerTarget != 0 {
		n++ // round up rather than silently undershooting the requested total
	}
	return n
}

// totalSeries is what the target will actually end up holding.
func (a metricScrapeLoadConfig) totalSeries() int {
	return seriesPerTarget * a.targetCount()
}

// samplesPerSecond is what the target has to ingest: one sample per active
// series per scrape interval.
func (a metricScrapeLoadConfig) samplesPerSecond() float64 {
	interval, err := time.ParseDuration(a.ScrapeInterval)
	if err != nil || interval <= 0 {
		return 0
	}
	return float64(a.totalSeries()) / interval.Seconds()
}

// avalancheSeriesPerInstance is the rule of thumb for how many active series
// one avalanche process can render without falling behind: avalanche
// serializes rendering behind a single mutex and re-renders the whole body
// on every scrape with no caching, so past this point scrapes start
// queueing behind each other, and a queued scrape times out and looks like
// the target failing.
const avalancheSeriesPerInstance = 1_000_000

// instanceCount is how many avalanche processes to run: one per
// avalancheSeriesPerInstance active series. Derived rather than configured
// so that active_series can be the only field anyone needs to set.
func (a metricScrapeLoadConfig) instanceCount() int {
	n := a.totalSeries() / avalancheSeriesPerInstance
	if a.totalSeries()%avalancheSeriesPerInstance != 0 {
		n++
	}
	if n < 1 {
		n = 1
	}
	// Never more instances than targets -- an instance nothing scrapes is
	// just an idle process competing for CPU with the thing being measured.
	if n > a.targetCount() {
		n = a.targetCount()
	}
	return n
}

// args renders one avalanche instance's command line. The metric shape is
// fixed (see seriesPerTarget), not read from config -- only active_series
// and scrape_interval are meant to vary between runs. series-interval and
// metric-interval are pinned at 0 deliberately: avalanche's own default for
// series-interval is 60, which replaces the whole series set every interval
// instead of just moving values, and cost one of our runs 13GB of RSS and an
// OOM kill in about twelve minutes.
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

// avalanchePorts is the port each instance listens on: consecutive from
// avalanchePort.
func avalanchePorts(av metricScrapeLoadConfig) []int {
	ports := make([]int, av.instanceCount())
	for i := range ports {
		ports[i] = avalanchePort + i
	}
	return ports
}

// startAvalanches runs one avalanche process per port, all with identical
// flags apart from the port, as the load the target scrapes. Configured by
// the `metric_scrape_load:` section of common.yml
// (see metricScrapeLoadConfig.args).
//
// Any instance that fails to start leaves the already-started ones for the
// caller to clean up: the error return is paired with the slice of what did
// start, so a deferred stopAvalanches still reaps them.
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
			return started, fmt.Errorf("port %d: is avalanche installed? (go install github.com/prometheus-community/avalanche/cmd/avalanche@latest): %w", port, err)
		}
		started = append(started, cmd)
	}
	return started, nil
}

// stopAvalanches kills every avalanche and waits for each to actually exit.
// They're stateless, so an unclean kill costs nothing -- but waiting matters:
// the next test in a batch binds these same fixed ports almost immediately,
// and a kill that hasn't been reaped yet can still be holding one.
func stopAvalanches(cmds []*exec.Cmd) {
	for _, cmd := range cmds {
		if cmd.Process == nil {
			continue
		}
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}
}

// writeAvalancheTargets writes the file_sd file listing every logical scrape
// target, at the fixed avalancheTargetsPath (see its own comment for why
// fixed).
//
// One group per target, each holding a single address and its own `instance`
// label, because a group's labels apply to the whole group. The addresses
// repeat -- targetCount targets round-robin over however many avalanche
// instances there are -- and that's the point: Prometheus identifies a target
// by its whole label set, so the same endpoint under a different `instance`
// is a separate target contributing its own full copy of avalanche's series.
// An explicitly-set `instance` overrides the address-derived default.
func writeAvalancheTargets(av metricScrapeLoadConfig) error {
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

	return os.WriteFile(avalancheTargetsPath, b.Bytes(), 0o644)
}
