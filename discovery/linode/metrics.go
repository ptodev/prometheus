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

package linode

import (
	"fmt"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/prometheus/discovery"
)

var _ discovery.DiscovererDebugMetrics = (*linodeMetrics)(nil)

type linodeMetrics struct {
	refreshMetrics discovery.RefreshDebugMetricsInstantiator

	failuresCount prometheus.Counter

	metricRegisterer discovery.MetricRegisterer
}

func newDiscovererDebugMetrics(reg prometheus.Registerer, rdmm discovery.RefreshDebugMetricsInstantiator) discovery.DiscovererDebugMetrics {
	m := &linodeMetrics{
		refreshMetrics: rdmm,
		failuresCount: prometheus.NewCounter(
			prometheus.CounterOpts{
				Name: "prometheus_sd_linode_failures_total",
				Help: "Number of Linode service discovery refresh failures.",
			}),
	}

	m.metricRegisterer = discovery.NewMetricRegisterer(reg, []prometheus.Collector{
		m.failuresCount,
	})

	return m
}

func convertToLinodeMetrics(metrics discovery.DiscovererDebugMetrics) (*linodeMetrics, error) {
	m, ok := metrics.(*linodeMetrics)
	if !ok {
		return nil, fmt.Errorf("invalid discovery metrics type")
	}
	return m, nil
}

// Register implements discovery.DiscovererMetrics.
func (m *linodeMetrics) Register() error {
	return m.metricRegisterer.RegisterMetrics()
}

// Unregister implements discovery.DiscovererMetrics.
func (m *linodeMetrics) Unregister() {
	m.metricRegisterer.UnregisterMetrics()
}
