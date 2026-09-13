# Stateless MCP and source inventory

Status: inspected uncommitted implementation on `codex/m4-runtime`, based on
`948fd14fb7ae04f2aeb12b36eebb13fe41acf1d9`. No source delivery, merge or release is
claimed. This is the directive's implementation context, not completion evidence.

## Current exported seams

| Surface | Behavior |
| --- | --- |
| `mcpapp.Options.StatelessHTTP` | Present in the inspected implementation as an opt-in; remove it before delivery so all HTTP serving is stateless. |
| `Application.RefreshWorkflow(ctx, identity, sessionID)` | Authorizes and replays the stored session without changing its attachment stamp. |
| `application.SnapshotFile` | Carries a filesystem-relative path and whether the snapshot loader treats it as a document. |
| `application.WalkSnapshotFiles(ctx, fsys, graphDir)` | Iterates the common file classification without reading file content. |

## Required adjustment before source delivery

Remove `StatelessHTTP` from the exported and internal composition options so every HTTP server uses SDK stateless serving. The field is uncommitted and unreleased, so its removal breaks no released API. Keep stdio unchanged and retain the existing local HTTP command. Validate the local HTTP composition and example against the change.

The root and example modules use MCP Go SDK v1.7.0. Under its stateless HTTP semantics, the server does not emit or depend on `Mcp-Session-Id`; GET and DELETE return 405, while POST can still stream SSE responses. Server-to-client requests are unavailable; the current SDD tools do not need them. The closing wrapper rejects new HTTP requests regardless of a transport-session header.

Authentication remains composition-owned. Tests that previously expected the SDK's transport-session identity check to return HTTP 403 must instead prove through application authorization that another authenticated person cannot access the SDD session. The access restriction remains.

Stateless requests do not retain MCP initialization state, so `mcpClientName` and `mcpClientVersion` may be empty. These fields are informational and control neither authorization nor workflow progression. Accept unavailable values rather than introducing transport state to preserve them.

## One dialogue across server processes

1. Server A opens S and persists the workflow events through `SessionStore`.
2. Server B receives a tool call carrying S, authorizes its caller, and reconstructs
   the latest stored workflow before executing the tool.
3. A later call on A repeats that refresh instead of trusting A's earlier object.
   A replacement process follows the same path using S.

`pkg/mcpapp/tools.go` performs the refresh for attached-session tool requests on
all transports. It preserves persisted served-content deduplication. Replay errors,
ended sessions and authorization failures stop execution rather than using a cached
workflow. Explicit `resume_session` retains its existing attachment/reset behavior.
Direct application consumers holding a workflow object must request refresh when
they need current stored state; adding the method does not refresh those objects
automatically.

`pkg/application/workflow.go` no longer retries stale workflow events after merely
refreshing the store version. It returns the typed session conflict with resume
guidance. Existing attachment-metadata and branch-binding retry behavior remains.
This does not add cross-process locking or whole-tool-call atomicity.

Refresh shares the existing replay path, including `ensureShell` compatibility for
legacy sessions without a shell. It is therefore not a promise that every refresh
performs zero writes. Ordinary initialized-session refresh preserves its attachment
stamp and served memory.

## One graph interpretation across source adapters

`WalkSnapshotFiles` owns the path classification also used by `LoadSnapshotFS`.
It yields file paths relative to the supplied filesystem root, including `graphDir`.
`Document` identifies structurally eligible entry Markdown and WIP documents;
frontmatter validation still happens during loading. Traversal skips nested graph
metadata as before and stops on consumer break, cancellation or a filesystem error.
Directory enumeration remains filesystem I/O; file content is not read by the walker.

An external adapter can enumerate one acquired revision, transfer document bytes
plus attachment names, and let OSS materialize the graph with `LoadSnapshotFS`.
The acquired `AttachmentPageReader` fetches attachment bytes from that same revision
when requested. The local loader uses the same inventory and avoids opening Markdown
attachment bodies merely to discover them. Malformed entry documents still produce
the existing health findings; document-read failures remain errors.

No new acquisition lease or serialized graph format is introduced. Configuration,
revision selection, authorization and release keep the existing acquired-view contract.

## Validation available before source delivery

Focused OSS tests cover:

- `TestStatelessHTTPAlternatesWarmReplicasAndRestarts`: independent servers sharing
  a local session store; alternating requests, process replacement, persisted served
  memory, explicit resume, changed identity, ended sessions and closing responses.
- `TestWarmSessionReplayErrorStopsTool`: failed replay cannot fall back to warm state.
- `TestRefreshWorkflowPreservesAttachmentAndServedMemory` and
  `TestRefreshWorkflowDiscardsFailedAppendAtUnchangedVersion`: refresh semantics and
  discarding rejected in-memory input before a later advance.
- Snapshot inventory tests: classification without opening content, early stop,
  cancellation and filesystem errors; loader tests preserve attachment names,
  document errors and malformed-document health reporting.

The external composition has also passed a separate PostgreSQL-backed test with
independent application/server instances, alternating requests, process replacement,
current authorization and membership revocation. Its source adapter uses the shared
inventory over an acquired Git revision. A real Claude web client has opened a
session and read entries, chains and attachment content, and used search through
that composition. This does not establish compatibility with every client or store.

OSS root/example tests, vet and lint passed earlier in this implementation, as did
the external composition's required checks. They were not rerun to prepare this
record. Completion must identify the final source revision and validation after
review fixes. Live source-service restart and broader client onboarding are not
claimed as completed by this record.

## Adoption

- Use HTTP as a stateless transport and keep local clients on stdio where appropriate; keep authenticated caller identity on every request and durable session storage shared between servers.
- Expect no `Mcp-Session-Id`, GET or DELETE support; POST can still stream SSE responses. Do not depend on server-to-client requests.
- Treat client name and version as optional informational metadata. Enforce access through the authenticated caller and application session authorization.
- Carry the SDD session handle across requests. It is independent of transport
  session headers. Keep explicit resume behavior distinct from per-request refresh.
- Use `WalkSnapshotFiles` when transferring a source for OSS materialization; supply
  attachment bytes through the acquired reader rather than the bulk graph transfer.
- Preserve existing write APIs and handle session conflicts through the surfaced
  resume guidance. This slice supplies no new generic mutation-retry API.
