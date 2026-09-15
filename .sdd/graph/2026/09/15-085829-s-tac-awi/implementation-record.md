# Session-based capture publication: implementation and validation record

Branch `codex/session-capture-publication`, PR 15, head commit `0814fd3c`. Recorded 2026-09-15 at implementation completion; not merged or released at that time.

## Delivered behavior

- Session events hold accepted input, explicit preflight results, staged filename-to-blob mappings, mutation intent and mutation outcome. The intent event records the invocation, the concrete target and the generated entry ID before publication runs. Replay reconstructs pending, completed and cancelled positions without dispatching anything.
- Session stores accept only one of competing appends at an event position, refuse empty appends, and assign consecutive sequences and timestamps. Metadata is informational. A rejected intent append dispatches nothing and no stale events are resubmitted.
- Capture preflight is an explicit operation whose acceptance is invalidated by changed draft fields, restaged attachments or a changed target. Creation receives the recorded entry ID and looks for an existing publication before summary generation or staged-blob reads. Preparation may repeat while no publication exists.
- Publication identity is a hash over session, intent-event sequence and operation discriminator, identical in local Git and external compositions. It is an identity, not a payload digest.
- For a Git-backed target the graph store writes the entry and its attachments; the Git finalizer is the sole committer and marks the commit `SDD-Mutation: v1:<hash>`. The store recognizes a committed publication by that trailer. No pre-release trailer format is read. Files on disk without a commit are not a publication.
- Required finalizers run on every attempt, including a retry that found an existing publication. Retrying a committed capture returns its original revision without another commit.
- A failed operation advertises exact `retry_ref` and `cancel_ref` values for `next`. Retry uses the stored input and returns the ordinary successful response. One pending intent gates session progression until retry or cancellation resolves it.
- Cancellation records its outcome and return position, performs no Git cleanup, returns to the preceding interaction without re-execution, or closes the instance when none preceded it. A published entry may remain; a fresh confirmation creates a new intent and a new entry. Abandonment is available once the pending intent is resolved.
- Staged bytes are immutable session resources. Filename mappings come from events; separate blob metadata and retention ledgers are removed. Collection frees a collectible session's staged bytes before deleting the session.
- A session ending derived from events for logs that predate the ending event takes the act and reason from the shell's terminal event; a log with no shell keeps its recorded ending.

## Composition and tests

- `internal/cliapp.New` builds the command tree with supplied input, output and error streams; `cmd/sdd/main.go` owns arguments, process streams, signals and exit codes. `mcpapp.Server.Run` accepts a supplied transport.
- `local.NewRepositoryTargets` is the production read and mutation target wiring, shared by the CLI and exercised by the local tests.
- CLI tests run in process over one fixture: command and flag wiring, configuration, stream separation, an MCP initialization over pipes, and reuse of the machine-global search index across fresh `serve` commands. Application write tests share one fixture and call `CreateEntry` directly for validation cases. Shipped capture behavior is tested in `proctest`; Git behavior in `local`.

## Validation evidence

| Owner | Evidence |
|---|---|
| Engine | `internal/engine/mutation_internal_test.go`: a failed intent append dispatches nothing; replay after a lost outcome; cancellation; stale references. |
| Application | `pkg/application/capture_test.go`: same-ID retry, lookup before preparation, required-finalizer retry, separate preflight. `session_legacy_end_test.go`: ending derived from the shell's terminal act and reason. |
| Shipped capture | `internal/proctest/capture_staging_test.go`, `capture_cancellation_test.go`: staged attachment and preflight state, cancellation with published effects, fresh confirmation, abandonment afterwards. |
| Local Git | `pkg/local/capture_publication_test.go`, `repository_targets_test.go`, `git_finalizer_test.go`: files without a commit are not a publication, failed commit then retry keeps the first rendered document, concurrent same-intent attempts commit once, whole-discriminator matching, and `CreateEntry` over the production wiring. |
| MCP | `pkg/mcpapp/retry_test.go`, `transport_test.go`: fresh-server replay, advertised references, the `next` surface, transport shutdown on disconnect or cancellation. |
| Storage parity | Shared session and staged-blob conformance suites plus the extension example: append ordering, nonempty appends, timestamps, immutable blob IDs including restaging a filename. |

Root and extension-example suites, vet and the lint wrapper passed at the head commit. A hosted external composition completed a real capture with an attachment; after its first push failed, the retry pushed the original commit and attachment without another capture, verified by independent Git readback. This demonstrates composition use, not a real-client lost-reply test. A real local-agent capture with the rebuilt binary remains pending.

## Corrections made in review

- The first commit wrote the raw session handle into Git trailers; the second introduced a second trailer name and readers for the first's format. Both readers and the second name were removed: no release wrote either, and the trailer name main has always used carries the hashed batch ID.
- The graph store no longer commits; the finalizer is the single committer, so the local target no longer wires one finalizer into two roles.
- The event-derived session ending overwrote a recorded one and mapped every shell end to a conclude. Checked against all ended sessions on one development machine: every one was a conclude with no reason, so none changed. Fixed for abandoned endings and for logs without a shell.
- Test relocation: CLI composition tests moved out of the main package, the target test drives `CreateEntry` instead of restating its sequence, write fixtures consolidated, the persistent-index restart test restored in process.

## Remaining scope

- Summary replacement and WIP writes still use the prepared-write path; their migration, the document-level summary precondition, the WIP closing-evidence check, collection's pending-write protection, bounded automatic retry inside the request, and cancellation naming its concrete residue are not delivered here.
- The filesystem-only graph store without a Git finalizer recognizes retries by entry ID alone; publication-key ownership is not guaranteed there. Kept deliberately. Normal local MCP capture uses Git-backed targets; the extension example and the procedure test world run the filesystem-only store.
- Entry-ID generation is unchanged.
- Collection logs an unreadable ending and leaves it untouched but omits it from the returned skipped count.
- An external consumer adopts this by moving its session store to event-tip concurrency with nonempty, timestamped appends, implementing staged-blob stage, open and delete, supplying an entry publication store on acquired mutation targets, passing the recorded entry ID, publication key, target and staged references to `CreateEntry`, calling preflight separately, and using the advertised `next` retry and cancel modes.
