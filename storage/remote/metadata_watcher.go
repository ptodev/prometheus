// Copyright The Prometheus Authors
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

package remote

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/prometheus/common/model"
	"github.com/prometheus/common/promslog"

	"github.com/prometheus/prometheus/scrape"
	"github.com/prometheus/prometheus/storage"
)

// MetadataAppender is an interface used by the Metadata Watcher to send metadata, It is read from the scrape manager, on to somewhere else.
type MetadataAppender interface {
	AppendWatcherMetadata(context.Context, []scrape.MetricMetadata)
}

// Watchable represents from where we fetch active targets for metadata.
type Watchable interface {
	TargetsActive() map[string][]*scrape.Target
}

type noopScrapeManager struct{}

func (*noopScrapeManager) Get() (*scrape.Manager, error) {
	return nil, errors.New("scrape manager not ready")
}

// MetadataWatcher watches the Scrape Manager for a given WriteMetadataTo.
type MetadataWatcher struct {
	name   string
	logger *slog.Logger

	managerGetter    ReadyScrapeManager
	manager          Watchable
	metadataLister   storage.MetadataLister
	writer           MetadataAppender

	interval model.Duration
	deadline time.Duration

	done chan struct{}

	softShutdownCtx    context.Context
	softShutdownCancel context.CancelFunc
	hardShutdownCancel context.CancelFunc
	hardShutdownCtx    context.Context
}

// NewMetadataWatcher builds a new MetadataWatcher.
func NewMetadataWatcher(l *slog.Logger, mg ReadyScrapeManager, name string, w MetadataAppender, interval model.Duration, deadline time.Duration) *MetadataWatcher {
	if l == nil {
		l = promslog.NewNopLogger()
	}

	if mg == nil {
		mg = &noopScrapeManager{}
	}

	return &MetadataWatcher{
		name:   name,
		logger: l,

		managerGetter: mg,
		writer:        w,

		interval: interval,
		deadline: deadline,

		done: make(chan struct{}),
	}
}

// Start the MetadataWatcher.
func (mw *MetadataWatcher) Start() {
	mw.logger.Info("Starting scraped metadata watcher")
	mw.hardShutdownCtx, mw.hardShutdownCancel = context.WithCancel(context.Background())
	mw.softShutdownCtx, mw.softShutdownCancel = context.WithCancel(mw.hardShutdownCtx)
	go mw.loop()
}

// Stop the MetadataWatcher.
func (mw *MetadataWatcher) Stop() {
	mw.logger.Info("Stopping metadata watcher...")
	defer mw.logger.Info("Scraped metadata watcher stopped")

	mw.softShutdownCancel()
	select {
	case <-mw.done:
		return
	case <-time.After(mw.deadline):
		mw.logger.Error("Failed to flush metadata")
	}

	mw.hardShutdownCancel()
	<-mw.done
}

func (mw *MetadataWatcher) loop() {
	ticker := time.NewTicker(time.Duration(mw.interval))
	defer ticker.Stop()
	defer close(mw.done)

	for {
		select {
		case <-mw.softShutdownCtx.Done():
			return
		case <-ticker.C:
			mw.collect()
		}
	}
}

// SetMetadataLister configures the watcher to collect metadata from a
// MetadataLister instead of polling scrape targets. When set, the scrape
// manager is not required. This decouples metadata collection from
// scraping, enabling systems like Grafana Alloy where scrape and remote
// write are independent components.
func (mw *MetadataWatcher) SetMetadataLister(l storage.MetadataLister) {
	mw.metadataLister = l
}

func (mw *MetadataWatcher) collect() {
	var metadata []scrape.MetricMetadata

	if mw.metadataLister != nil {
		entries := mw.metadataLister.ListMetadata()
		metadata = make([]scrape.MetricMetadata, len(entries))
		for i, e := range entries {
			metadata[i] = scrape.MetricMetadata{
				MetricFamily: e.MetricFamily,
				Type:         e.Type.Type,
				Help:         e.Type.Help,
				Unit:         e.Type.Unit,
			}
		}
	} else {
		if !mw.ready() {
			return
		}
		metadataSet := map[scrape.MetricMetadata]struct{}{}
		for _, tset := range mw.manager.TargetsActive() {
			for _, target := range tset {
				for _, entry := range target.ListMetadata() {
					if _, ok := metadataSet[entry]; !ok {
						metadata = append(metadata, entry)
						metadataSet[entry] = struct{}{}
					}
				}
			}
		}
	}

	if len(metadata) == 0 {
		return
	}

	// Blocks until the metadata is sent to the remote write endpoint or hardShutdownContext is expired.
	mw.writer.AppendWatcherMetadata(mw.hardShutdownCtx, metadata)
}

func (mw *MetadataWatcher) ready() bool {
	if mw.manager != nil {
		return true
	}

	m, err := mw.managerGetter.Get()
	if err != nil {
		return false
	}

	mw.manager = m
	return true
}
