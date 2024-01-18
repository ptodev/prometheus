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

package dns

import (
	"fmt"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/prometheus/discovery"
	"github.com/prometheus/prometheus/discovery/refresh"
)

var _ discovery.DiscovererDebugMetrics = (*dnsMetrics)(nil)

type dnsMetrics struct {
	refreshMetrics discovery.RefreshDebugMetricsInstantiator

	dnsSDLookupsCount        prometheus.Counter
	dnsSDLookupFailuresCount prometheus.Counter

	metricRegisterer discovery.MetricRegisterer
}

func newDiscovererDebugMetrics(reg prometheus.Registerer, rdmm discovery.RefreshDebugMetricsInstantiator) discovery.DiscovererDebugMetrics {
	m := &dnsMetrics{
		refreshMetrics: refresh.NewDiscovererDebugMetrics(reg, "dns"),
		dnsSDLookupsCount: prometheus.NewCounter(
			prometheus.CounterOpts{
				Namespace: namespace,
				Name:      "sd_dns_lookups_total",
				Help:      "The number of DNS-SD lookups.",
			}),
		dnsSDLookupFailuresCount: prometheus.NewCounter(
			prometheus.CounterOpts{
				Namespace: namespace,
				Name:      "sd_dns_lookup_failures_total",
				Help:      "The number of DNS-SD lookup failures.",
			}),
	}

	m.metricRegisterer = discovery.NewMetricRegisterer(reg, []prometheus.Collector{
		m.dnsSDLookupsCount,
		m.dnsSDLookupFailuresCount,
	})

	return m
}

func convertToDnsMetrics(metrics discovery.DiscovererDebugMetrics) (*dnsMetrics, error) {
	m, ok := metrics.(*dnsMetrics)
	if !ok {
		return nil, fmt.Errorf("invalid discovery metrics type")
	}
	return m, nil
}

// Register implements discovery.DiscovererMetrics.
func (m *dnsMetrics) Register() error {
	if err := m.refreshMetrics.Register(); err != nil {
		return err
	}
	return m.metricRegisterer.RegisterMetrics()
}

// Unregister implements discovery.DiscovererMetrics.
func (m *dnsMetrics) Unregister() {
	m.refreshMetrics.Unregister()
	m.metricRegisterer.UnregisterMetrics()
}
