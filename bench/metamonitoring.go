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

//go:embed alloy.alloy.tmpl
var alloyConfigTemplate string

var alloyTmpl = template.Must(template.New("alloy").Parse(alloyConfigTemplate))

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

func writeAlloyConfig(runDir, label, targetLogPath string, monitoring monitoringConfig) (string, error) {
	receiveHost, receivePort, err := net.SplitHostPort(alloyReceiveAddr)
	if err != nil {
		return "", err
	}

	var b strings.Builder
	err = alloyTmpl.Execute(&b, alloyConfigData{
		Label:         label,
		TargetAddr:    targetAddr,
		TargetLogPath: targetLogPath,
		ReceiveHost:   receiveHost,
		ReceivePort:   receivePort,
		Pyroscope:     monitoring.Pyroscope,
		Loki:          monitoring.Loki,
		Prometheus:    monitoring.Prometheus,
	})
	if err != nil {
		return "", err
	}

	path := filepath.Join(runDir, "alloy.alloy")
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		return "", err
	}
	return filepath.Abs(path)
}

func startAlloy(ctx context.Context, runDir, cfgPath string) (*exec.Cmd, error) {
	dataDir := filepath.Join(runDir, "alloy-data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, "alloy", "run", cfgPath,
		"--server.http.listen-addr="+alloyAddr,
		"--storage.path="+dataDir,
		"--stability.level=public-preview")
	logF, err := os.Create(filepath.Join(runDir, "alloy.log"))
	if err != nil {
		return nil, err
	}
	cmd.Stdout, cmd.Stderr = logF, logF
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	if err := cmd.Start(); err != nil {
		logF.Close()
		return nil, fmt.Errorf("is alloy installed? (see README.md): %w", err)
	}
	logF.Close()
	return cmd, nil
}

func stopAlloy(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
}
