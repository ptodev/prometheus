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

package moby

import (
	"fmt"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/prometheus/discovery"
	"github.com/prometheus/prometheus/discovery/refresh"
)

var _ discovery.DiscovererDebugMetrics = (*dockerMetrics)(nil)

type dockerswarmMetrics struct {
	refreshMetrics discovery.RefreshDebugMetricsInstantiator
}

func newDiscovererDebugMetricsDockerswarm(reg prometheus.Registerer) discovery.DiscovererDebugMetrics {
	m := &dockerMetrics{
		refreshMetrics: refresh.NewDiscovererDebugMetrics(reg, "dockerswarm"),
	}

	return m
}

func convertToDockerswarmMetrics(metrics discovery.DiscovererDebugMetrics) (*dockerswarmMetrics, error) {
	m, ok := metrics.(*dockerswarmMetrics)
	if !ok {
		return nil, fmt.Errorf("invalid discovery metrics type")
	}
	return m, nil
}

// Register implements discovery.DiscovererMetrics.
func (m *dockerswarmMetrics) Register() error {
	return m.refreshMetrics.Register()
}

// Unregister implements discovery.DiscovererMetrics.
func (m *dockerswarmMetrics) Unregister() {
	m.refreshMetrics.Unregister()
}
