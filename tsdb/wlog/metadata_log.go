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

package wlog

import (
	"sync"

	"github.com/prometheus/prometheus/tsdb/record"
)

// MetadataLog is an in-memory ordered log of metadata entries. The appender
// writes entries when metadata changes, and the WAL watcher drains them
// alongside WAL records. This provides the same ordering guarantees as WAL
// metadata records without writing metadata to disk.
//
// When Prometheus adds persistent WAL metadata records, this structure can
// be removed — the WAL watcher will read metadata from disk instead.
type MetadataLog struct {
	mu      sync.Mutex
	entries []record.RefMetadata
}

// Append adds metadata entries to the log. Called by the appender at commit
// time, alongside WAL writes.
func (l *MetadataLog) Append(entries []record.RefMetadata) {
	if len(entries) == 0 {
		return
	}
	l.mu.Lock()
	l.entries = append(l.entries, entries...)
	l.mu.Unlock()
}

// Drain returns all pending entries and clears the log. Called by the WAL
// watcher after processing WAL records.
func (l *MetadataLog) Drain() []record.RefMetadata {
	l.mu.Lock()
	entries := l.entries
	l.entries = nil
	l.mu.Unlock()
	return entries
}
