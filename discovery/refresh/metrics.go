// Copyright 2015 The Prometheus Authors
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package refresh

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/prometheus/discovery"
)

type debugMetricsVecs struct {
	failuresVec *prometheus.CounterVec
	durationVec *prometheus.SummaryVec

	metricRegisterer discovery.MetricRegisterer
}

var _ discovery.RefreshDebugMetricsManager = (*debugMetricsVecs)(nil)

func NewDebugMetrics(reg prometheus.Registerer) discovery.RefreshDebugMetricsManager {
	m := &debugMetricsVecs{
		failuresVec: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "prometheus_sd_refresh_failures_total",
				Help: "Number of refresh failures for the given SD mechanism.",
			},
			[]string{"mechanism"}),
		durationVec: prometheus.NewSummaryVec(
			prometheus.SummaryOpts{
				Name:       "prometheus_sd_refresh_duration_seconds",
				Help:       "The duration of a refresh in seconds for the given SD mechanism.",
				Objectives: map[float64]float64{0.5: 0.05, 0.9: 0.01, 0.99: 0.001},
			},
			[]string{"mechanism"}),
	}

	m.metricRegisterer = discovery.NewMetricRegisterer(reg, []prometheus.Collector{
		m.failuresVec,
		m.durationVec,
	})

	return m
}

func (m *debugMetricsVecs) Instantiate(mech string) *discovery.RefreshDebugMetrics {
	return &discovery.RefreshDebugMetrics{
		Failures: m.failuresVec.WithLabelValues(mech),
		Duration: m.durationVec.WithLabelValues(mech),
	}
}

// Register implements discovery.DiscovererMetrics.
func (m *debugMetricsVecs) Register() error {
	return m.metricRegisterer.RegisterMetrics()
}

// Unregister implements discovery.DiscovererMetrics.
func (m *debugMetricsVecs) Unregister() {
	m.metricRegisterer.UnregisterMetrics()
}
