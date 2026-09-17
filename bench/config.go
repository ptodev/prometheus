package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// testsDir is where `go run .` looks for tests when none are named explicitly.
const testsDir = "tests"

// commonPath holds settings shared by every test. Factored out of test.yml
// once both checked-in tests turned out to want identical values for them.
const commonPath = testsDir + "/common.yml"

// subjectUnderTestConfig identifies the binary being benchmarked.
type subjectUnderTestConfig struct {
	// Type is the kind of binary.
	// Supported values: "prometheus", "prometheus_agent".
	// TODO: "alloy", "otel_collector".
	Type string `yaml:"type"`

	// BinaryPath is the executable to benchmark: absolute path or a name on
	// $PATH. In common.yml rather than per-test because both checked-in tests
	// want the same one: they differ by a flag, not by binary.
	BinaryPath string `yaml:"binary_path"`
}

// common is testsDir/common.yml. There is no per-test override: a test.yml
// that also sets one of these fields fails with an unknown-field error
// rather than silently overriding or merging.
type common struct {
	// SubjectUnderTest identifies the binary being benchmarked.
	SubjectUnderTest subjectUnderTestConfig `yaml:"subject_under_test"`

	Duration string `yaml:"duration"` // parsed with time.ParseDuration

	// Gap is how long to idle between tests in a batch, so one test's series
	// have gone quiet before the next starts writing. Worth at least the
	// query lookback (5m in Prometheus and Mimir alike): a series with no new
	// samples still resolves for that long, since nothing writes staleness
	// markers when a target simply shuts down, so without a gap the previous
	// test's lines run into the next one's on every panel. Only applied
	// between tests, never before the first or after the last, and skipped
	// when a test fails -- a failure means debugging, not measuring.
	Gap string `yaml:"gap"`

	// Avalanche is the metric scrape load. Every field has a default (see
	// defaultCommon), so a common.yml can set as few or as many as it likes.
	Avalanche metricScrapeLoadConfig `yaml:"metric_scrape_load"`

	Agent bool `yaml:"agent"`

	// Monitoring groups the three sinks that observability data is forwarded to.
	Monitoring monitoringConfig `yaml:"monitoring"`
}

// endpoint is one place data is sent. User and APIKey are Basic Auth
// credentials, and both are optional: leave them out for an unauthenticated
// endpoint. For Grafana Cloud, user is the numeric instance ID of that
// service and api_key an access-policy token with write scope for it -- note
// that the three services have different instance IDs.
type endpoint struct {
	URL    string `yaml:"url"`
	User   string `yaml:"user"`
	APIKey string `yaml:"api_key"`
}

// monitoringConfig groups the three observability sinks.
type monitoringConfig struct {
	// Prometheus receives the target's metrics: the target remote_writes to
	// Alloy's local receiver (see alloyReceiveAddr), and Alloy forwards here.
	// A test's own prometheus.yml declares that local remote_write itself --
	// bench never writes to it (see checkTargetConfig).
	Prometheus endpoint `yaml:"prometheus"`
	// Loki receives the target's log file.
	Loki endpoint `yaml:"loki"`
	// Pyroscope receives the target's profiles.
	Pyroscope endpoint `yaml:"pyroscope"`
}

// metricScrapeLoadConfig is the `metric_scrape_load:` section of common.yml.
// active_series is the only field worth setting -- the metric shape
// avalanche generates (see seriesPerTarget) is fixed, not configurable, and
// the scrape interval lives in the test's own prometheus.yml instead (see
// ScrapeInterval below), not here, since that's the only place a real
// value can live without drifting from what Prometheus actually does.
//
// Methods and process-management for this type live in
// metric_scrape_load.go, not here -- this file holds config shapes and
// loading, that one holds everything about actually running avalanche.
type metricScrapeLoadConfig struct {
	// ActiveSeries is the total the target should end up holding, across
	// every scraped target. 0 means "just one target", i.e. seriesPerTarget
	// in total.
	ActiveSeries int `yaml:"active_series"`

	// ScrapeInterval is the interval avalanche will actually be scraped at.
	// Not decoded from common.yml: populated by loadTest from the test's own
	// prometheus.yml (see checkTargetConfig), which is the one place
	// Prometheus itself is actually configured with this number. A second
	// copy in common.yml could silently drift from it or do nothing at all.
	ScrapeInterval string `yaml:"-"`

	// Timeout bounds how long each avalanche instance gets to become ready
	// before this run is considered to have failed to start. It pre-generates
	// its (fixed) ~10,000 series before answering its first request, which
	// takes a small fraction of a second on any reasonable machine -- this
	// budget is for a slow or loaded one, not for a bigger workload, since
	// the per-instance shape no longer varies.
	Timeout string `yaml:"timeout"`
}

// Test is a test directory's test.yml: only what actually varies between
// tests. Everything shared lives in common.yml instead (loadTest fills in
// the fields below from there, tagged `yaml:"-"` so decoding test.yml itself
// never accepts them).
type Test struct {
	// Label tags this run's data in Prometheus/Loki/Pyroscope. Defaults to the
	// test directory's name.
	Label string `yaml:"label"`

	// ExtraArgs are appended to the target's command line verbatim, e.g.
	// ["--enable-feature=metadata-wal-records"]. A YAML list rather than one
	// space-separated string, so an argument containing a space needs no
	// shell-style quoting.
	ExtraArgs []string `yaml:"extra_args"`

	// Filled in from common.yml, not decoded from this test's own test.yml.
	Bin        string                 `yaml:"-"`
	Duration   string                 `yaml:"-"`
	Avalanche  metricScrapeLoadConfig `yaml:"-"`
	Agent      bool                   `yaml:"-"`
	Prometheus endpoint               `yaml:"-"`
	Loki       endpoint               `yaml:"-"`
	Pyroscope  endpoint               `yaml:"-"`
}

// cachedCommon holds tests/common.yml once loaded, so a batch of tests reads
// it once rather than once per test.
var cachedCommon *common

// defaultCommon is what a common.yml starts from before decoding.
func defaultCommon() common {
	return common{
		Gap: "5m",
		Avalanche: metricScrapeLoadConfig{
			ActiveSeries: 0, // one target, seriesPerTarget in total
			Timeout:      "30s",
		},
	}
}

// loadCommon reads tests/common.yml.
func loadCommon() (common, error) {
	if cachedCommon != nil {
		return *cachedCommon, nil
	}
	b, err := os.ReadFile(commonPath)
	if err != nil {
		return common{}, fmt.Errorf("reading settings shared by every test: %w", err)
	}
	// Decoded over the defaults, not into a zero value: yaml.v3 only assigns
	// fields the document actually mentions, so an omitted avalanche setting
	// keeps its default while an explicit `series_interval: 0` still means 0.
	c := defaultCommon()
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil {
		return common{}, fmt.Errorf("%s: %w", commonPath, err)
	}
	if _, err := time.ParseDuration(c.Avalanche.Timeout); err != nil {
		return common{}, fmt.Errorf("%s: metric_scrape_load.timeout: %w", commonPath, err)
	}
	if _, err := time.ParseDuration(c.Gap); err != nil {
		return common{}, fmt.Errorf("%s: gap: %w", commonPath, err)
	}
	if c.SubjectUnderTest.BinaryPath == "" {
		return common{}, fmt.Errorf("%s: subject_under_test.binary_path is not set (the executable to benchmark)", commonPath)
	}
	// Checked up front rather than at first use: a missing URL would
	// otherwise surface as a failed push an hour into the run, with the data
	// for that hour already gone.
	for _, e := range []struct {
		field string
		url   string
	}{
		{"monitoring.prometheus", c.Monitoring.Prometheus.URL},
		{"monitoring.loki", c.Monitoring.Loki.URL},
		{"monitoring.pyroscope", c.Monitoring.Pyroscope.URL},
	} {
		if e.url == "" {
			return common{}, fmt.Errorf("%s: %s.url is not set (where should the data go?)", commonPath, e.field)
		}
	}
	cachedCommon = &c
	return c, nil
}

// loadTest reads <dir>/test.yml and checks <dir>/prometheus.yml. bench never
// writes to that file (see checkTargetConfig): the rest of a test's config
// is exactly what its author wrote, unmodified, and the process is pointed
// straight at it.
func loadTest(dir string) (Test, string, error) {
	c, err := loadCommon()
	if err != nil {
		return Test{}, "", err
	}

	testPath := filepath.Join(dir, "test.yml")
	b, err := os.ReadFile(testPath)
	if err != nil {
		return Test{}, "", err
	}

	var t Test
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true) // a typo'd key is an error, not a silently ignored one
	if err := dec.Decode(&t); err != nil {
		return Test{}, "", fmt.Errorf("%s: %w", testPath, err)
	}
	t.Bin, t.Duration, t.Agent = c.SubjectUnderTest.BinaryPath, c.Duration, c.Agent
	t.Avalanche = c.Avalanche
	t.Prometheus, t.Loki, t.Pyroscope = c.Monitoring.Prometheus, c.Monitoring.Loki, c.Monitoring.Pyroscope

	if t.Label == "" {
		t.Label = filepath.Base(dir)
	}

	cfgPath := filepath.Join(dir, "prometheus.yml")
	scrapeInterval, err := checkTargetConfig(cfgPath)
	if err != nil {
		return Test{}, "", err
	}
	t.Avalanche.ScrapeInterval = scrapeInterval
	return t, cfgPath, nil
}

// checkTargetConfig confirms a test's prometheus.yml declares what bench
// needs it to -- the avalanche scrape job (file_sd_configs pointed at
// avalancheTargetsPath) and a remote_write pointed at Alloy's receiver
// (alloyReceiveAddr) -- and returns the interval avalanche will actually be
// scraped at: the avalanche job's own scrape_interval if it sets one, else
// global.scrape_interval, else Prometheus's own default of 1m.
//
// Read-only, and deliberately so: bench used to parse this file and
// re-marshal it with an injected remote_write and scrape job, which meant
// the config that actually ran was never quite the checked-in file, and the
// real remote_write destination (with real credentials from common.yml)
// only existed in a generated copy. Now the target remote_writes to a fixed,
// unauthenticated local address that Alloy receives from and forwards on
// with the real credentials -- so the checked-in file needs no per-run
// values at all, and this function only ever reads it.
//
// Failing here, before anything starts, is deliberate: a test directory
// copied without updating both blocks would otherwise silently run with no
// load, or with metrics going nowhere.
func checkTargetConfig(cfgPath string) (string, error) {
	b, err := os.ReadFile(cfgPath)
	if err != nil {
		return "", err
	}
	var cfg struct {
		Global struct {
			ScrapeInterval string `yaml:"scrape_interval"`
		} `yaml:"global"`
		ScrapeConfigs []struct {
			JobName        string `yaml:"job_name"`
			ScrapeInterval string `yaml:"scrape_interval"`
		} `yaml:"scrape_configs"`
		RemoteWrite []struct {
			URL string `yaml:"url"`
		} `yaml:"remote_write"`
	}
	// Not KnownFields: this is Prometheus's schema, not one bench defines, so
	// a field bench doesn't know about is the test author's business.
	if err := yaml.Unmarshal(b, &cfg); err != nil {
		return "", fmt.Errorf("%s: %w", cfgPath, err)
	}

	var hasAvalancheJob bool
	interval := cfg.Global.ScrapeInterval
	for _, sc := range cfg.ScrapeConfigs {
		if sc.JobName == "avalanche" {
			hasAvalancheJob = true
			if sc.ScrapeInterval != "" {
				interval = sc.ScrapeInterval
			}
		}
	}
	if interval == "" {
		interval = "1m" // Prometheus's own default
	}
	if !hasAvalancheJob {
		return "", fmt.Errorf("%s: no scrape_configs job named \"avalanche\" -- bench needs one with "+
			"file_sd_configs pointed at %q (see tests/metadata/prometheus.yml)", cfgPath, avalancheTargetsPath)
	}

	var hasAlloyRemoteWrite bool
	for _, rw := range cfg.RemoteWrite {
		if strings.Contains(rw.URL, alloyReceiveAddr) {
			hasAlloyRemoteWrite = true
		}
	}
	if !hasAlloyRemoteWrite {
		return "", fmt.Errorf("%s: no remote_write pointed at http://%s -- bench forwards from there "+
			"through Alloy to the real endpoint (see tests/metadata/prometheus.yml)", cfgPath, alloyReceiveAddr)
	}
	return interval, nil
}
