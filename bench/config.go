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
	testsDir   = "tests"
	commonPath = testsDir + "/common.yml"
)

type subjectConfig struct {
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
	ActiveSeries int    `yaml:"active_series"`
	Timeout      string `yaml:"timeout"`
}

type testConfig struct {
	Subject   subjectConfig `yaml:"subject_under_test"`
	ExtraArgs []string      `yaml:"extra_args"`
}

func loadCommon() (common, error) {
	c := common{
		Gap:       "5m",
		Avalanche: metricScrapeLoadConfig{Timeout: "30s"},
	}
	if err := decodeYAML(commonPath, &c, true); err != nil {
		return common{}, err
	}
	if c.Subject.BinaryPath == "" {
		return common{}, fmt.Errorf("%s: subject_under_test.binary_path is required", commonPath)
	}
	for name, value := range map[string]string{
		"duration":                   c.Duration,
		"gap":                        c.Gap,
		"metric_scrape_load.timeout": c.Avalanche.Timeout,
	} {
		if _, err := time.ParseDuration(value); err != nil {
			return common{}, fmt.Errorf("%s: %s: %w", commonPath, name, err)
		}
	}
	for name, e := range map[string]endpoint{
		"prometheus": c.Monitoring.Prometheus,
		"loki":       c.Monitoring.Loki,
		"pyroscope":  c.Monitoring.Pyroscope,
	} {
		if e.URL == "" {
			return common{}, fmt.Errorf("%s: monitoring.%s.url is required", commonPath, name)
		}
	}
	return c, nil
}

func loadTest(dir string) (testConfig, string, string, error) {
	path := filepath.Join(dir, "test.yml")
	var test testConfig
	if err := decodeYAML(path, &test, true); err != nil {
		return testConfig{}, "", "", err
	}

	configPath := filepath.Join(dir, "prometheus.yml")
	interval, err := checkTargetConfig(configPath)
	if err != nil {
		return testConfig{}, "", "", err
	}
	return test, configPath, interval, nil
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
