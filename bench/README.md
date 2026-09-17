# bench

Runs a Prometheus binary against some load for a while, and sends the result
to your Grafana stack. The target remote_writes its own metrics to a local
receiver in Grafana Alloy, which bench also starts; Alloy forwards those on
to Prometheus with the real credentials, pulls the target's CPU/heap
profiles into Pyroscope, tails its log file into Loki, and forwards this
machine's own metrics -- via an in-process node_exporter -- to Prometheus
too. None of that ever needs a secret in a checked-in file.

```
main.go               command entry point
run.go                orchestration and process lifecycle
config.go             configuration loading and validation
metric_scrape_load.go avalanche: its config, process lifecycle, target list
metamonitoring.go      Alloy: its config, process lifecycle
alloy.alloy.tmpl       Alloy's config, as a template (embedded into metamonitoring.go)
dashboard.json        a small dashboard using Prometheus's own metric names
go.mod, go.sum
tests/
  common.yml.example  copy to common.yml and fill in your endpoints
  common.yml           gitignored: holds the API keys, the binary, the load
  <name>/
    test.yml             what's different about this one: label, flags
    prometheus.yml       the target's own config -- bench never writes to it
avalanche_targets.json   gitignored, regenerated every run (fixed name, see below)
runs/                 gitignored: each run's config, logs, profiles, Alloy config
```

Requires `avalanche` and `alloy` on `$PATH` alongside the binary under test
-- see the Debian VM setup below for installing both.

## Run it locally

```sh
cd bench
cp tests/common.yml.example tests/common.yml   # then fill it in
go run .                                       # every test under tests/, in order
```

`common.yml` holds everything shared: the binary to benchmark, how long to
run, how much load, and where the data goes. It is gitignored because it
holds API keys; `common.yml.example` is the checked-in copy documenting every
field.

To run a subset of tests:

```sh
go run . tests/metadata                    # just this one
go run . tests/metadata tests/no-metadata  # in this order
```

All runs land in the same Grafana stack, tagged with each test's `label`.

## Run it on a Debian VM

### Setup the VM

```sh
sudo apt-get update && sudo apt-get install -y git tmux unzip
GO=$(curl -s https://go.dev/VERSION?m=text | head -1)   # e.g. go1.25.1
curl -LO "https://go.dev/dl/$GO.linux-amd64.tar.gz"
sudo tar -C /usr/local -xzf "$GO.linux-amd64.tar.gz"

# ~/.bashrc, not ~/.profile: tmux panes are non-login interactive shells and
# read .bashrc only, so a PATH set in .profile is missing inside tmux -- which
# is where the long runs happen. $HOME/go/bin is spelled out rather than
# $(go env GOPATH)/bin because that subshell needs go on PATH already, and
# would otherwise expand to a broken "/bin".
echo 'export PATH=$PATH:/usr/local/go/bin:$HOME/go/bin' >> ~/.bashrc
source ~/.bashrc

go install github.com/prometheus-community/avalanche/cmd/avalanche@latest

# Alloy has no `go install` path (its build uses a Makefile, not plain go
# build); grab a release binary instead. See
# https://grafana.com/docs/alloy/latest/get-started/install/ for every OS and
# package manager -- this is the generic Linux tarball route.
ALLOY_VERSION=$(curl -s https://api.github.com/repos/grafana/alloy/releases/latest | grep -o '"tag_name": *"[^"]*"' | cut -d'"' -f4)
curl -LO "https://github.com/grafana/alloy/releases/download/${ALLOY_VERSION}/alloy-linux-amd64.zip"
unzip alloy-linux-amd64.zip && chmod +x alloy-linux-amd64
mv alloy-linux-amd64 $HOME/go/bin/alloy

# If you need a specific version rather than latest, grab that asset
# directly instead of the two commands above. Check the VM's own
# architecture first -- `uname -m` -- and match it: `x86_64` is amd64,
# `aarch64` is arm64. Getting this wrong downloads fine but fails at run
# time with "cannot execute binary file: Exec format error". Example for
# v1.19.2 on amd64 (`uname -m` == x86_64):
curl -LO https://github.com/grafana/alloy/releases/download/v1.19.2/alloy-linux-amd64.zip
# or, with wget:
wget https://github.com/grafana/alloy/releases/download/v1.19.2/alloy-linux-amd64.zip
unzip alloy-linux-amd64.zip && chmod +x alloy-linux-amd64
mv alloy-linux-amd64 $HOME/go/bin/alloy
# -- swap amd64 for arm64 throughout if `uname -m` says aarch64 instead.

git clone <this repo> prometheus && cd prometheus
go build -o /tmp/prometheus ./cmd/prometheus   # the binary under test
cd bench && cp tests/common.yml.example tests/common.yml
# set subject_under_test.binary_path and the monitoring endpoints, then see below
```

### Run it under tmux

Running under tmux makes it easy to get back to the test if your session
disconnected (e.g. laptop went to sleep).

```sh
tmux new -s bench
cd ~/prometheus/bench && go run .
# Ctrl-b then d to detach; the run carries on without you
tmux attach -t bench                      # reconnect, any time, from anywhere
```

### Cleaning up stray processes

```sh
pkill -f 'prometheus|avalanche|alloy'
```

## Test options

A test is a directory with two files:

- **`test.yml`** — what makes this test different: its label and any extra
  command-line flags. That's all: two fields.
- **`prometheus.yml`** — the target's own config, checked in and run exactly
  as written. Bench validates but does not rewrite it. No self-scrape: the target's own health is scraped by
  Alloy instead (see Monitoring below), so the target's own head and WAL
  hold nothing but avalanche-driven series -- what's actually under test.
  It must declare, and bench checks for at startup:
  - a scrape job named `avalanche` with `file_sd_configs` pointed at
    `avalanche_targets.json` (a fixed path bench regenerates every run --
    see `tests/metadata/prometheus.yml` for the exact shape);
  - a `remote_write` pointed at `http://127.0.0.1:12346/api/v1/write` --
    Alloy's local, unauthenticated receiver, which forwards on to the real
    endpoint with the real credentials from `common.yml`. No secret is ever
    in this checked-in file.

  avalanche's own scrape interval -- which sets both the ingest rate and
  avalanche's own render cost -- comes from this file too
  (`global.scrape_interval`, or the `avalanche` job's own override), not
  from `common.yml`: it's the one place Prometheus is actually configured
  with the number, so it can't drift from what's really happening.

Everything else — the binary under test, duration, load, monitoring
endpoints — lives once in **`tests/common.yml`**. There's no per-test
override: a `test.yml` that also sets one of those fields is a hard error
(unknown field), not a silent merge. If a test genuinely needs a different
value there, that's worth changing deliberately, not something to half-build
an override system for speculatively.

Make a new test by copying a directory. `test.yml`:

```yaml
extra_args:   # extra command-line flags appended verbatim to the target binary
  - --enable-feature=some-flag
```

`tests/common.yml`, shared by every test:

```yaml
subject_under_test:
  type: prometheus          # "prometheus" or "prometheus_agent"
  binary_path: /tmp/prometheus

duration: 2h

metric_scrape_load:
  active_series: 1000000

monitoring:
  prometheus:
    url: https://...
    user: "123456"        # optional; Grafana Cloud: numeric instance ID
    api_key: glc_...      # optional; Grafana Cloud: access-policy token
  loki:
    url: https://...
  pyroscope:
    url: https://...
```

**`subject_under_test`**

| Field | | |
|---|---|---|
| `type` | required | `prometheus` or `prometheus_agent` |
| `binary_path` | required | absolute path or name on `$PATH` |

**`metric_scrape_load`**

| Field | Default | |
|---|---|---|
| `active_series` | `0` (one target, ~10,000 series) | total series to generate |

The scrape interval isn't here -- it lives in each test's own
`prometheus.yml` instead (see Test options below), since that's the one
place it can't drift from what Prometheus actually does.

In normal use `active_series` is the only field you set:

```yaml
metric_scrape_load:
  active_series: 10000000
```

The shape of one target is fixed, not configurable: mostly gauges and
counters with some histograms, roughly what a heavy exporter looks like
(kube-state-metrics, cadvisor, a big node_exporter) --
`4 x (300 gauge + 300 counter + 100 histogram x10 buckets + 300 native_histogram x3) = 10,000`
series per target, and `num_targets = ceil(active_series / 10,000)`.

Every target is the *same* avalanche endpoint under a different `instance`
label. Prometheus identifies a target by its whole label set
(`scrape/target.go:158-166`), so each is scraped independently and
contributes its own full copy of the series. That's what makes millions of
active series affordable: avalanche only ever generates 10,000 of them, no
matter how high `active_series` goes. One avalanche process handles up to
1,000,000 active series before bench starts another -- past that, scrapes
queue behind each other and start looking like target failures instead.

**`monitoring`** — `prometheus`, `loki`, and `pyroscope` each take:

| Field | | |
|---|---|---|
| `url` | required | |
| `user` | optional | Basic Auth username (Grafana Cloud: numeric instance ID) |
| `api_key` | optional | Basic Auth password (Grafana Cloud: access-policy token with write scope) |

Unknown fields are rejected rather than silently ignored, and a missing
`subject_under_test.binary_path` or `url` fails before anything starts rather than an hour in.

All three endpoints are reached only through Alloy (started alongside the
target), which reuses these same three -- there's no separate Alloy config.
The target remote_writes to a fixed local address
(`http://127.0.0.1:12346/api/v1/write`, declared in the test's own
`prometheus.yml`) that Alloy receives from and forwards to `prometheus`.
Alloy also:

- scrapes the target's own `/metrics` for its health (tsdb head size,
  process CPU/memory, remote-write queue stats, ...), `job="target"` --
  not a self-scrape, so the target's own head and WAL hold nothing but
  avalanche-driven series;
- scrapes this machine's own metrics via an in-process node_exporter,
  `job="host"`;
- forwards the target's profiles to `pyroscope`;
- tails the target's logs into `loki`.

In Grafana: target metrics are `{job="target", test="<label>"}`; target logs
are `{job="target", run="<label>"}`; profiles are under service `<label>`
(profile types `process_cpu`, `memory`); host metrics are `{job="host",
test="<label>"}`. Log tailing is continuous, not a batch push at the end, so
an interrupted run still has whatever it logged.

