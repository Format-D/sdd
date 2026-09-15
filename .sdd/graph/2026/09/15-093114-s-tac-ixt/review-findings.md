# Review findings retained after PR 15 (merged as 547ef763)

Locations as of the merge commit. Each line: where, what, why it matters.

## Production

- `pkg/application/workflow_capture.go` `stagedAt`, `capturePreflightState`: reload the session and decode raw engine event payloads to recover staged mappings and preflight state; a second staged-filename projection exists beside `w.staged`. Engine owns replay; it should expose the positional view.
- `pkg/application/workflow.go` `workflowSink.Append`: returns `binding.Version + 1` as the event position instead of the store-assigned sequence. Holds only because the version CAS forces them equal.
- `pkg/application/workflow.go` `WorkflowPendingOperation`, `WorkflowCancellation`: carry MCP json tags and are embedded verbatim into the wire result; every other served type is mapped in `mcpapp`.
- `internal/engine/mutation.go` `ContinuationInstructions`: tool-call syntax in engine-produced text; procedure text must stay host-neutral (20260707-134311-d-cpt-476).
- `pkg/application/graphstore.go` `MutationBatch.Digest`, `pkg/local/local_graphstore.go` `Apply` size and digest revalidation: withdrawn by 20260914-113911-d-tac-wgw, still computed and enforced on the prepared-write path; the capture path leaves the fields empty.
- `internal/engine/registry.go` `GraphIndependent`: no registered command reads the graph, so the flag distinguishes nothing.
- `internal/engine` `appendEvent`: swallows the sink error into `sinkErr`; thirteen `checkSink` call sites re-check it.
- `internal/engine/mutation.go` `applyCancellation`: ends an instance without an abandoned event; two projections special-case the terminal state.
- `pkg/application/capture.go`: two `MutationBatch` constructions for one publication, with different commit messages.
- `pkg/application/capture.go` `PreflightEntry`: `binding` parameter unused. `EntryDraft.SkipPreflight` only stamps the entry. `CreateEntryResult.Findings` is never set. `newEntry` `Doc.Writes` declares fields the command never writes.
- Capture procedure spec: the `write` step re-asks preflight state already checked; its `otherwise` arm is unreachable.

## Tests

- `pkg/mcpapp/session_store_test.go`, `retry_test.go`: import `internal/engine` and replay a full capture lifecycle; transport tests should assert only reference handling.
- `pkg/application/session_legacy_end_test.go` `TestCancellationOutcomeUpdatesSessionListing`, `collect_test.go` `TestCollectDoesNotExecutePendingWrites`: hand-write engine event JSON; the latter passes with pending-write handling removed.
- `pkg/application/collect_test.go` `stagedIDs`: probes only blobs the fixture staged; the orphan-detection test was removed with it.
- `internal/engine/mutation_internal_test.go`: touches only exported symbols, no seam justifies the internal package; `TestRecordedInvocationReplay` replays in-memory events and skips the JSON boundary.
- Three `Sink` fakes in engine tests with three position policies, while position is now identity.
- Duplicated fixtures: two fail-once finalizers, two `collectFixture` types asserting different things about one contract.
- `internal/proctest/capture_cancellation_test.go`: one test asserts eleven behaviors; `harness_test.go` runs subtests mid-sequence that would corrupt the world on failure.
- Coverage: no external-composition test of capture with attachment, interrupted publication or same-entry retry; competing appends tested only sequentially.
