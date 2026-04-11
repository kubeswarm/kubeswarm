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

import "context"

// AuditSink is the interface for audit event backends. Implementations must be
// safe for concurrent use. Emit receives a batch of events and returns an error
// if the write fails - the emitter handles retry and drop logic.
type AuditSink interface {
	Emit(ctx context.Context, events []AuditEvent) error
}
