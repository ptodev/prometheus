// Command bench runs a Prometheus binary against a load generator for a fixed
// duration, and sends everything a person would want to look at afterward to
// a Grafana stack. The target remote_writes its own metrics directly; Grafana
// Alloy, which bench also starts, pulls its CPU/heap profiles into Pyroscope,
// tails its log file into Loki, and forwards this machine's own metrics --
// via an in-process node_exporter -- to Prometheus. Credentials for all of it
// are in tests/common.yml -- see README.md.
//
// The load comes from avalanche (github.com/prometheus-community/avalanche),
// run as one or more child processes. The target scrapes them like any other
// exporter -- many times over, once per logical target, to reach the
// requested active-series count.
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

	// alloyReceiveAddr is where Alloy's prometheus.receive_http listens for
	// the target's own remote_write. Loopback and unauthenticated: real
	// credentials live only in Alloy's own remote_write to the configured
	// Prometheus endpoint, never in a checked-in file.
	alloyReceiveAddr = "127.0.0.1:12346"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "bench:", err)
		os.Exit(1)
	}
}

func run() error {
	flag.Usage = func() {
		fmt.Fprint(os.Stderr, "usage: bench [test-dir ...]\n\n"+
			"With no arguments, runs every test under "+testsDir+"/, in order.\n"+
			"Name one or more directories to run only those.\n\n"+
			"example:\n"+
			"  go run .                  # every test under "+testsDir+"/\n"+
			"  go run . tests/metadata   # just this one\n")
	}
	flag.Parse()

	testDirs := flag.Args()
	if len(testDirs) == 0 {
		var err error
		testDirs, err = discoverTests(testsDir)
		if err != nil {
			return err
		}
	}

	// Loaded here as well as per-test so a bad common.yml fails once, before
	// anything starts, rather than identically for every test in the batch.
	c, err := loadCommon()
	if err != nil {
		return err
	}
	gap, _ := time.ParseDuration(c.Gap) // validated in loadCommon

	// One shared context: Ctrl-C during any test stops the whole batch, not
	// just the one running.
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
		err := runTest(ctx, dir)
		results = append(results, outcome{dir, err})

		if err == nil && gap > 0 && i < len(testDirs)-1 {
			fmt.Printf("\nwaiting %s so this test's series go stale before the next one starts ...\n", gap)
			select {
			case <-time.After(gap):
			case <-ctx.Done():
			}
		}
	}

	// A single named test just returns its own error; a batch gets a summary
	// line per test first, so a failure part way through is easy to spot
	// against the ones that passed.
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

// discoverTests finds every subdirectory of dir that has a test.yml, in the
// order os.ReadDir returns them (alphabetical).
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

// runTest runs one test directory end to end: load its config, start
// avalanche and the target, wait out the duration, stop, and save the result.
func runTest(ctx context.Context, testDir string) error {
	test, cfgPath, err := loadTest(testDir)
	if err != nil {
		return err
	}
	duration, err := time.ParseDuration(test.Duration)
	if err != nil {
		return fmt.Errorf("%s: duration: %w", testDir, err)
	}
	// Already validated in loadCommon, so this can't fail here.
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

	// Printed before anything starts, so a run that can't possibly fit is
	// obvious now rather than an hour in.
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
	// Deferred before the error check and before the readiness wait below, so
	// whatever did start is still reaped on any return path -- Go runs
	// deferred calls on an error return too. Waits for each process to
	// actually exit, not just for the signal: the next test in a batch binds
	// these same fixed ports almost immediately, and a kill that hasn't been
	// reaped yet can still be holding one.
	defer stopAvalanches(avs)
	if err != nil {
		return fmt.Errorf("starting avalanche: %w", err)
	}

	// avalanche pre-generates every series before it can answer its first
	// request -- 200k series takes about 1.5s under typical load.
	// metric_scrape_load.timeout in common.yml controls how long each
	// instance gets; raise it on a slow or loaded machine.
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
	// The target died on its own -- almost always the OOM killer on a machine
	// too small for series x metrics. Fail immediately instead of sleeping out
	// the rest of the duration on a process that isn't there: the run is void
	// either way, and on a batch of hour-long tests that wasted hour is the
	// difference between finding out now and finding out tomorrow. Alloy has
	// been tailing the log live, so its lines are already in Loki regardless.
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

// startTarget starts the binary under test, using the test's own
// prometheus.yml completely unmodified -- see checkTargetConfig for why.
// A copy is still written into the run directory and the process pointed at
// that, so a run's config is recorded exactly as used and can't be affected
// by someone editing the checked-in file while the run is still going.
//
// Agent mode needs a different storage flag than server mode:
// --storage.tsdb.path is server-only and the process refuses to start in
// agent mode with it.
func startTarget(bin, runDir, cfgPath string, test Test) (*targetProc, error) {
	cfgCopy := filepath.Join(runDir, "prometheus.yml")
	orig, err := os.ReadFile(cfgPath)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(cfgCopy, orig, 0o644); err != nil {
		return nil, err
	}

	// Every run starts from an empty storage directory. Left in place, the
	// previous run's WAL and blocks get replayed at startup: the head comes up
	// pre-populated, memory starts high, and the first minutes measure a
	// replay rather than the load. Worse for an A/B, the two tests then differ
	// by whatever each happened to inherit -- a test following an aborted run
	// replays several seconds of WAL while the other starts clean, and that
	// difference shows up in exactly the panels being compared.
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

	// exec.Command, not exec.CommandContext: a cancelled context must not
	// SIGKILL the target. That leaves the WAL dirty, which is exactly the kind
	// of thing a benchmark should not be doing to itself. stopTarget always
	// sends SIGTERM first.
	cmd := exec.Command(bin, args...)
	cmd.Stdout, cmd.Stderr = logF, logF
	if err := cmd.Start(); err != nil {
		logF.Close()
		return nil, err
	}

	// One Wait, once, in one place: calling cmd.Wait twice is an error, and
	// both the duration timer and stopTarget need to know when the process
	// ended. Buffered so this goroutine can't leak if nobody reads it.
	tp := &targetProc{cmd: cmd, logPath: logPath, done: make(chan error, 1)}
	go func() { tp.done <- cmd.Wait() }()
	return tp, nil
}

// sinkAddr is where the built-in discard sink listens. Point
// prometheus.url at http://127.0.0.1:9099/receive to use it.
const sinkAddr = "127.0.0.1:9099"

// discardSink is a remote_write endpoint that reads every request and throws
// it away, for runs whose series count is far beyond what any real backend
// would accept -- millions of active series would be rejected by a hosted
// tenant's limit, and indexing them into a local Prometheus just moves the
// memory problem next door.
//
// Nothing parses the payload: the sender only needs a 2xx, so snappy and
// protobuf never have to be decoded. It still measures the real cost on the
// target's side -- serialisation, compression, queueing, HTTP -- over
// loopback, so what's missing versus a remote backend is network latency and
// whatever backpressure a real server would apply.
type discardSink struct {
	srv      *http.Server
	requests atomic.Int64
	bytes    atomic.Int64
}

// startDiscardSink starts the sink, or returns nil if url doesn't point at
// it -- so configuring a real backend simply doesn't run one.
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

// stop shuts the sink down and reports what it swallowed. The counts are the
// only evidence remote_write actually got through, since by design nothing is
// stored.
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

// targetProc is the running binary under test.
type targetProc struct {
	cmd     *exec.Cmd
	logPath string
	// done receives the process's exit status exactly once, whether it was
	// asked to stop or died on its own. Watching it is what lets a run abort
	// on an OOM kill instead of profiling a process that no longer exists.
	done chan error
}

// stopTarget sends SIGTERM and waits, falling back to SIGKILL only if it
// doesn't exit in time.
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

// logTail returns the last n lines of a file, for putting the reason a run
// failed into the error itself rather than making someone go and find it.
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

// waitReady polls a URL until it answers 200.
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

