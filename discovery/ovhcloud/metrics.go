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

package ovhcloud

import (
	"fmt"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/prometheus/discovery"
)

var _ discovery.DiscovererDebugMetrics = (*ovhcloudMetrics)(nil)

type ovhcloudMetrics struct {
	refreshMetrics discovery.RefreshDebugMetricsInstantiator
}

func newDiscovererDebugMetrics(reg prometheus.Registerer, rdmm discovery.RefreshDebugMetricsInstantiator) discovery.DiscovererDebugMetrics {
	m := &ovhcloudMetrics{
		refreshMetrics: rdmm,
	}

	return m
}

func convertToOvhcloudMetrics(metrics discovery.DiscovererDebugMetrics) (*ovhcloudMetrics, error) {
	m, ok := metrics.(*ovhcloudMetrics)
	if !ok {
		return nil, fmt.Errorf("invalid discovery metrics type")
	}
	return m, nil
}

// Register implements discovery.DiscovererMetrics.
func (m *ovhcloudMetrics) Register() error {
	return nil
}

// Unregister implements discovery.DiscovererMetrics.
func (m *ovhcloudMetrics) Unregister() {}
