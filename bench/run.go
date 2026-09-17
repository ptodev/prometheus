package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

const (
	targetAddr = "127.0.0.1:9090"
	alloyAddr  = "127.0.0.1:12345"

	alloyReceiveAddr = "127.0.0.1:12346"
)

func run() error {
	flag.Usage = func() { fmt.Fprintln(os.Stderr, "usage: bench [test-dir ...]") }
	flag.Parse()

	testDirs := flag.Args()
	if len(testDirs) == 0 {
		var err error
		testDirs, err = discoverTests(testsDir)
		if err != nil {
			return err
		}
	}

	c, err := loadCommon()
	if err != nil {
		return err
	}
	gap, _ := time.ParseDuration(c.Gap)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	type outcome struct {
		dir string
		err error
	}
	var results []outcome
	for i, dir := range testDirs {
		if ctx.Err() != nil {
			fmt.Printf("interrupted, skipping remaining tests: %s\n", strings.Join(testDirs[i:], ", "))
			break
		}
		if len(testDirs) > 1 {
			fmt.Printf("\n=== [%d/%d] %s ===\n", i+1, len(testDirs), dir)
		}
		err := runTest(ctx, dir, c)
		results = append(results, outcome{dir, err})

		if err == nil && gap > 0 && i < len(testDirs)-1 {
			fmt.Printf("\nwaiting %s so this test's series go stale before the next one starts ...\n", gap)
			select {
			case <-time.After(gap):
			case <-ctx.Done():
			}
		}
	}

	if len(testDirs) == 1 {
		if len(results) == 0 {
			return fmt.Errorf("interrupted before starting")
		}
		return results[0].err
	}

	fmt.Println("\n=== summary ===")
	var failed int
	for _, r := range results {
		status := "ok"
		if r.err != nil {
			status = "FAILED: " + r.err.Error()
			failed++
		}
		fmt.Printf("%-20s %s\n", r.dir, status)
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d test(s) failed", failed, len(results))
	}
	return nil
}

func discoverTests(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("finding tests under %s: %w", dir, err)
	}
	var found []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		p := filepath.Join(dir, e.Name())
		if _, err := os.Stat(filepath.Join(p, "test.yml")); err == nil {
			found = append(found, p)
		}
	}
	if len(found) == 0 {
		return nil, fmt.Errorf("no tests found under %s (looking for */test.yml)", dir)
	}
	return found, nil
}

func runTest(ctx context.Context, testDir string, c common) error {
	test, cfgPath, err := loadTest(testDir, c)
	if err != nil {
		return err
	}
	duration, err := time.ParseDuration(test.Duration)
	if err != nil {
		return fmt.Errorf("%s: duration: %w", testDir, err)
	}
	avalancheTimeout, _ := time.ParseDuration(test.Avalanche.Timeout)

	binAbs, err := exec.LookPath(test.Bin)
	if err != nil {
		return fmt.Errorf("%s: subject_under_test.binary_path %q: %w", testDir, test.Bin, err)
	}

	runDir, err := filepath.Abs(filepath.Join("runs", test.Label))
	if err != nil {
		return err
	}
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		return err
	}
	fmt.Printf("run directory: %s\n", runDir)

	av := test.Avalanche
	fmt.Printf("avalanche: %d instance(s) x %d series, %d targets = %d active series @ %s\n",
		av.instanceCount(), seriesPerTarget, av.targetCount(), av.totalSeries(), av.ScrapeInterval)
	fmt.Printf("estimated cost: %.0f samples/sec to ingest\n", av.samplesPerSecond())

	if err := writeAvalancheTargets(av); err != nil {
		return fmt.Errorf("writing the avalanche target list: %w", err)
	}

	sink, err := startDiscardSink(test.Prometheus.URL)
	if err != nil {
		return err
	}
	defer sink.stop()
	if sink != nil {
		fmt.Printf("discard sink: listening on %s, storing nothing\n", sinkAddr)
	}

	avs, err := startAvalanches(ctx, runDir, av)
	defer stopAvalanches(avs)
	if err != nil {
		return fmt.Errorf("starting avalanche: %w", err)
	}

	for _, port := range avalanchePorts(av) {
		if err := waitReady(ctx, fmt.Sprintf("http://127.0.0.1:%d/metrics", port), avalancheTimeout); err != nil {
			return fmt.Errorf("avalanche on :%d never became ready within %s (see %s/avalanche-%d.log): %w",
				port, avalancheTimeout, runDir, port, err)
		}
	}

	target, err := startTarget(binAbs, runDir, cfgPath, test)
	if err != nil {
		return fmt.Errorf("starting the target: %w", err)
	}

	if err := waitReady(ctx, "http://"+targetAddr+"/-/ready", 2*time.Minute); err != nil {
		_ = stopTarget(target, 10*time.Second)
		return fmt.Errorf("target never became ready (see %s): %w", target.logPath, err)
	}
	fmt.Printf("target: ready, pid %d, log at %s\n", target.cmd.Process.Pid, target.logPath)

	alloyCfgPath, err := writeAlloyConfig(runDir, test, target.logPath)
	if err != nil {
		return fmt.Errorf("writing alloy config: %w", err)
	}
	alloyCmd, err := startAlloy(ctx, runDir, alloyCfgPath)
	if err != nil {
		return fmt.Errorf("starting alloy: %w", err)
	}
	defer stopAlloy(alloyCmd)
	if err := waitReady(ctx, "http://"+alloyAddr+"/metrics", 30*time.Second); err != nil {
		return fmt.Errorf("alloy never became ready within 30s (see %s/alloy.log): %w", runDir, err)
	}
	fmt.Println("alloy: running (profiles, logs, and this machine's own metrics)")

	fmt.Printf("running for %s (Ctrl-C to stop early) ...\n", duration)
	started := time.Now()
	select {
	case <-time.After(duration):
	case <-ctx.Done():
		fmt.Println("\ninterrupted, stopping early")
	case werr := <-target.done:
		return fmt.Errorf("target exited after %s, before its %s was up (%v). "+
			"Usually the OOM killer: check `journalctl -k | grep -i \"killed process\"` "+
			"and lower active_series in %s.\nlast log lines (full log at %s):\n%s",
			time.Since(started).Round(time.Second), duration, werr, commonPath,
			target.logPath, logTail(target.logPath, 15))
	}

	fmt.Println("stopping target ...")
	if err := stopTarget(target, 30*time.Second); err != nil {
		fmt.Fprintf(os.Stderr, "warning: %v\n", err)
	}

	fmt.Printf(`
done. in Grafana:

  Dashboard: pick %[1]q in the "test" variable to see it as its own
             legend-named line (import dashboard.json once, see README.md)
  Loki:      {job="target", run=%[1]q}
  Pyroscope: service %[1]q (profile types process_cpu, memory)
  Host:      {job="host", test=%[1]q} for this machine's own metrics
`, test.Label)
	return nil
}

func startTarget(bin, runDir, cfgPath string, test testConfig) (*targetProc, error) {
	cfgCopy := filepath.Join(runDir, "prometheus.yml")
	orig, err := os.ReadFile(cfgPath)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(cfgCopy, orig, 0o644); err != nil {
		return nil, err
	}

	dataDir := filepath.Join(runDir, "data")
	if err := os.RemoveAll(dataDir); err != nil {
		return nil, fmt.Errorf("clearing %s: %w", dataDir, err)
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, err
	}

	args := []string{
		"--config.file=" + cfgCopy,
		"--web.listen-address=" + targetAddr,
		"--log.format=logfmt",
	}
	if test.Agent {
		args = append(args, "--agent", "--storage.agent.path="+dataDir)
	} else {
		args = append(args, "--storage.tsdb.path="+dataDir)
	}
	args = append(args, test.ExtraArgs...)

	logPath := filepath.Join(runDir, "target.log")
	logF, err := os.Create(logPath)
	if err != nil {
		return nil, err
	}

	cmd := exec.Command(bin, args...)
	cmd.Stdout, cmd.Stderr = logF, logF
	if err := cmd.Start(); err != nil {
		logF.Close()
		return nil, err
	}
	logF.Close()

	tp := &targetProc{cmd: cmd, logPath: logPath, done: make(chan error, 1)}
	go func() { tp.done <- cmd.Wait() }()
	return tp, nil
}

const sinkAddr = "127.0.0.1:9099"

type discardSink struct {
	srv      *http.Server
	requests atomic.Int64
	bytes    atomic.Int64
}

func startDiscardSink(url string) (*discardSink, error) {
	if !strings.Contains(url, sinkAddr) {
		return nil, nil
	}
	s := &discardSink{}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		n, _ := io.Copy(io.Discard, r.Body)
		s.requests.Add(1)
		s.bytes.Add(n)
		w.WriteHeader(http.StatusNoContent)
	})
	s.srv = &http.Server{Addr: sinkAddr, Handler: mux}

	ln, err := net.Listen("tcp", sinkAddr)
	if err != nil {
		return nil, fmt.Errorf("discard sink on %s (is one already running?): %w", sinkAddr, err)
	}
	go func() { _ = s.srv.Serve(ln) }()
	return s, nil
}

func (s *discardSink) stop() {
	if s == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = s.srv.Shutdown(ctx)
	fmt.Printf("discard sink: %d requests, %.1f MiB received and dropped\n",
		s.requests.Load(), float64(s.bytes.Load())/(1<<20))
}

type targetProc struct {
	cmd     *exec.Cmd
	logPath string
	done chan error
}

func stopTarget(tp *targetProc, grace time.Duration) error {
	if err := tp.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		return err
	}
	select {
	case <-tp.done:
		return nil
	case <-time.After(grace):
		_ = tp.cmd.Process.Kill()
		<-tp.done
		return fmt.Errorf("did not exit within %s, had to SIGKILL", grace)
	}
}

func logTail(path string, n int) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return "(could not read log: " + err.Error() + ")"
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

func waitReady(ctx context.Context, url string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: 2 * time.Second}
	for {
		resp, err := client.Get(url)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s not ready after %s (last error: %v)", url, timeout, err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

