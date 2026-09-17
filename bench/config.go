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

const (
	testsDir  = "tests"
	commonPath = testsDir + "/common.yml"
)

type subjectConfig struct {
	Type       string `yaml:"type"`
	BinaryPath string `yaml:"binary_path"`
}

type common struct {
	Subject    subjectConfig          `yaml:"subject_under_test"`
	Duration   string                 `yaml:"duration"`
	Gap        string                 `yaml:"gap"`
	Avalanche  metricScrapeLoadConfig `yaml:"metric_scrape_load"`
	Monitoring monitoringConfig       `yaml:"monitoring"`
}

type endpoint struct {
	URL    string `yaml:"url"`
	User   string `yaml:"user"`
	APIKey string `yaml:"api_key"`
}

type monitoringConfig struct {
	Prometheus endpoint `yaml:"prometheus"`
	Loki       endpoint `yaml:"loki"`
	Pyroscope  endpoint `yaml:"pyroscope"`
}

type metricScrapeLoadConfig struct {
	ActiveSeries   int    `yaml:"active_series"`
	ScrapeInterval string `yaml:"-"`
	Timeout        string `yaml:"timeout"`
}

type testConfig struct {
	Label       string   `yaml:"label"`
	ExtraArgs   []string `yaml:"extra_args"`
	Bin         string   `yaml:"-"`
	Duration    string   `yaml:"-"`
	Avalanche   metricScrapeLoadConfig `yaml:"-"`
	Agent       bool     `yaml:"-"`
	Prometheus  endpoint `yaml:"-"`
	Loki        endpoint `yaml:"-"`
	Pyroscope   endpoint `yaml:"-"`
}

func loadCommon() (common, error) {
	c := common{
		Subject: subjectConfig{Type: "prometheus"},
		Gap:     "5m",
		Avalanche: metricScrapeLoadConfig{Timeout: "30s"},
	}
	if err := decodeYAML(commonPath, &c, true); err != nil {
		return common{}, err
	}
	if c.Subject.BinaryPath == "" {
		return common{}, fmt.Errorf("%s: subject_under_test.binary_path is required", commonPath)
	}
	if c.Subject.Type != "prometheus" && c.Subject.Type != "prometheus_agent" {
		return common{}, fmt.Errorf("%s: unsupported subject_under_test.type %q", commonPath, c.Subject.Type)
	}
	for name, value := range map[string]string{
		"duration": c.Duration,
		"gap": c.Gap,
		"metric_scrape_load.timeout": c.Avalanche.Timeout,
	} {
		if _, err := time.ParseDuration(value); err != nil {
			return common{}, fmt.Errorf("%s: %s: %w", commonPath, name, err)
		}
	}
	for name, e := range map[string]endpoint{
		"prometheus": c.Monitoring.Prometheus,
		"loki": c.Monitoring.Loki,
		"pyroscope": c.Monitoring.Pyroscope,
	} {
		if e.URL == "" {
			return common{}, fmt.Errorf("%s: monitoring.%s.url is required", commonPath, name)
		}
	}
	return c, nil
}

func loadTest(dir string, c common) (testConfig, string, error) {
	path := filepath.Join(dir, "test.yml")
	var t testConfig
	if err := decodeYAML(path, &t, true); err != nil {
		return testConfig{}, "", err
	}
	if t.Label == "" {
		t.Label = filepath.Base(dir)
	}
	t.Bin = c.Subject.BinaryPath
	t.Duration = c.Duration
	t.Agent = c.Subject.Type == "prometheus_agent"
	t.Avalanche = c.Avalanche
	t.Prometheus, t.Loki, t.Pyroscope = c.Monitoring.Prometheus, c.Monitoring.Loki, c.Monitoring.Pyroscope

	configPath := filepath.Join(dir, "prometheus.yml")
	interval, err := checkTargetConfig(configPath)
	if err != nil {
		return testConfig{}, "", err
	}
	t.Avalanche.ScrapeInterval = interval
	return t, configPath, nil
}

func decodeYAML(path string, dst any, strict bool) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(strict)
	if err := dec.Decode(dst); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

func checkTargetConfig(path string) (string, error) {
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
	if err := decodeYAML(path, &cfg, false); err != nil {
		return "", err
	}

	interval := cfg.Global.ScrapeInterval
	foundJob := false
	for _, scrape := range cfg.ScrapeConfigs {
		if scrape.JobName == "avalanche" {
			foundJob = true
			if scrape.ScrapeInterval != "" {
				interval = scrape.ScrapeInterval
			}
		}
	}
	if !foundJob {
		return "", fmt.Errorf("%s: scrape job %q is required", path, "avalanche")
	}
	if interval == "" {
		interval = "1m"
	}
	for _, write := range cfg.RemoteWrite {
		if strings.Contains(write.URL, alloyReceiveAddr) {
			return interval, nil
		}
	}
	return "", fmt.Errorf("%s: remote_write to http://%s is required", path, alloyReceiveAddr)
}
