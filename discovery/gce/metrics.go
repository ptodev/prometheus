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

package gce

import (
	"fmt"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/prometheus/discovery"
	"github.com/prometheus/prometheus/discovery/refresh"
)

var _ discovery.DiscovererDebugMetrics = (*gceMetrics)(nil)

type gceMetrics struct {
	refreshMetrics discovery.RefreshDebugMetricsInstantiator
}

func newDiscovererDebugMetrics(reg prometheus.Registerer, rdmm discovery.RefreshDebugMetricsInstantiator) discovery.DiscovererDebugMetrics {
	m := &gceMetrics{
		refreshMetrics: refresh.NewDiscovererDebugMetrics(reg, "gce"),
	}

	return m
}

func convertToGceMetrics(metrics discovery.DiscovererDebugMetrics) (*gceMetrics, error) {
	m, ok := metrics.(*gceMetrics)
	if !ok {
		return nil, fmt.Errorf("invalid discovery metrics type")
	}
	return m, nil
}

// Register implements discovery.DiscovererMetrics.
func (m *gceMetrics) Register() error {
	return m.refreshMetrics.Register()
}

// Unregister implements discovery.DiscovererMetrics.
func (m *gceMetrics) Unregister() {
	m.refreshMetrics.Unregister()
}
