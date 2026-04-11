/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package audit

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"sync"
)

// StdoutSink writes one JSON line per audit event to an io.Writer.
// It is safe for concurrent use.
type StdoutSink struct {
	mu sync.Mutex
	w  io.Writer
}

// NewStdoutSink creates a StdoutSink that writes to the given writer.
// If w is nil, os.Stdout is used.
func NewStdoutSink(w io.Writer) *StdoutSink {
	if w == nil {
		w = os.Stdout
	}
	return &StdoutSink{w: w}
}

// Emit serializes each event as a JSON line and writes it to the underlying writer.
func (s *StdoutSink) Emit(_ context.Context, events []AuditEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i := range events {
		data, err := json.Marshal(&events[i])
		if err != nil {
			return err
		}
		data = append(data, '\n')
		if _, err := s.w.Write(data); err != nil {
			return err
		}
	}
	return nil
}
