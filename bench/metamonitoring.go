// This file holds everything specific to metamonitoring the run itself --
// generating Alloy's config and its process lifecycle. alloyAddr and
// alloyReceiveAddr, used by main.go to start the target and wait for Alloy
// to come up, live in main.go instead, alongside the other fixed addresses
// bench's own processes agree on.
package main

import (
	"context"
	_ "embed"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"text/template"
)

// alloyConfigTemplate is alloy.alloy.tmpl, kept as its own file rather than
// Go string literals so the River syntax is readable and editable on its
// own terms -- no Go escaping, and named fields instead of positional
// verbs. The latter is not a hypothetical concern: an earlier version of
// this built the config with fmt.Fprintf and ~11 positional %q/%s verbs,
// and a routine edit once misaligned them against their args, caught only
// by `go vet` ("format %s reads arg #8, but call has 7 args").
//
//go:embed alloy.alloy.tmpl
var alloyConfigTemplate string

// alloyTmpl is parsed once at program start; a malformed template is a
// programmer error, not a runtime one, so template.Must is appropriate.
var alloyTmpl = template.Must(template.New("alloy").Parse(alloyConfigTemplate))

// alloyConfigData is alloy.alloy.tmpl's input.
type alloyConfigData struct {
	Label         string
	TargetAddr    string
	TargetLogPath string
	ReceiveHost   string
	ReceivePort   string
	Pyroscope     endpoint
	Loki          endpoint
	Prometheus    endpoint
}

// writeAlloyConfig generates the Alloy config for one test and returns its
// absolute path. Alloy is a separate process (github.com/grafana/alloy, see
// README.md) that does five things:
//
//   - receives the target's own remote_write (a fixed, unauthenticated
//     local address the target's checked-in prometheus.yml points at, see
//     checkTargetConfig) and forwards it on to the real Prometheus with the
//     real credentials -- this is the one bench itself used to inject a
//     remote_write block for, before;
//   - scrapes the target's own /metrics for its health (tsdb head size,
//     process CPU/memory, remote-write queue stats, ...) the same way it
//     scrapes this machine's own node_exporter below, rather than the
//     target self-scraping. That keeps the target's own head and WAL
//     holding only avalanche-driven series -- what's actually under test --
//     instead of a few hundred series describing itself, and keeps this
//     path (host metrics, target health) consistently out-of-band from the
//     path being measured (avalanche's scrape and the target's remote_write
//     of it);
//   - pulls the target's CPU and memory profiles from its own /debug/pprof
//     -- always present, no flag required -- and forwards them to Pyroscope;
//   - tails the target's log file into Loki, continuously rather than in
//     one batch at the end, so an interrupted run still has whatever it
//     logged up to that point;
//   - runs node_exporter in-process (prometheus.exporter.unix, no separate
//     binary) for this machine's own metrics, remote-written to Prometheus
//     and tagged with this test's label the same way the target's own
//     metrics are.
func writeAlloyConfig(runDir string, test Test, targetLogPath string) (string, error) {
	receiveHost, receivePort, err := net.SplitHostPort(alloyReceiveAddr)
	if err != nil {
		return "", err
	}

	var b strings.Builder
	err = alloyTmpl.Execute(&b, alloyConfigData{
		Label:         test.Label,
		TargetAddr:    targetAddr,
		TargetLogPath: targetLogPath,
		ReceiveHost:   receiveHost,
		ReceivePort:   receivePort,
		Pyroscope:     test.Pyroscope,
		Loki:          test.Loki,
		Prometheus:    test.Prometheus,
	})
	if err != nil {
		return "", err
	}

	// 0o600: this copy has credentials in it, like the target's own generated
	// prometheus.yml.
	path := filepath.Join(runDir, "alloy.alloy")
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		return "", err
	}
	return filepath.Abs(path)
}

// startAlloy starts Alloy against a generated config, pointed at its own
// data directory for component state (loki.source.file and pyroscope.scrape
// both track their own read/scrape position there).
func startAlloy(ctx context.Context, runDir, cfgPath string) (*exec.Cmd, error) {
	dataDir := filepath.Join(runDir, "alloy-data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, "alloy", "run", cfgPath,
		"--server.http.listen-addr="+alloyAddr,
		"--storage.path="+dataDir,
		// prometheus.exporter.unix ships behind a non-generally-available
		// stability gate as of this writing; adjust if a newer Alloy moves it.
		"--stability.level=public-preview")
	logF, err := os.Create(filepath.Join(runDir, "alloy.log"))
	if err != nil {
		return nil, err
	}
	cmd.Stdout, cmd.Stderr = logF, logF
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("is alloy installed? (see README.md): %w", err)
	}
	return cmd, nil
}

// stopAlloy kills Alloy and waits for it to actually exit. It has no
// meaningful state of its own to lose -- everything it forwards lives at the
// source or the destination, not in Alloy -- so an unclean kill costs
// nothing.
func stopAlloy(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
}
