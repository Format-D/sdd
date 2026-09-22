# The MCP test surface as measured (a83f130b)

## 1. What the suite asserts

| | |
| --- | --- |
| `strings.Contains` assertions in `pkg/mcpapp` tests | 164, across 68 of 98 test functions |
| readable snapshot tests (cupaloy) in the whole repository | 1 file — `internal/presenters/show_test.go` |
| MCP tool call sites in those tests | ~300, covering all 12 tools |
| tests asserting a **complete** tool response | 0 |
| `go test -count=1 ./pkg/mcpapp/` | 12.4s, over the real engine and procedures |

Per file: `server_test.go` 114, `principles_framing_test.go` 9, `retry_test.go` 8,
`view_test.go` 7, `multiproject_test.go` 6, `drafting_knowledge_test.go` 6,
`view_helpers_test.go` 4, `serve_dedup_test.go` 4, `stateless_http_test.go` 3,
`request_identity_test.go` 3.

What the 164 match against:

| class | count | target |
| --- | --- | --- |
| error messages | 53 | `msg`, `message` |
| served prose | 77 | `Instructions` (46), `Framing` (31) |
| read results | 10 | `shown.Entries`, `res.Results` |
| other | 24 | vocabulary, locals |

`TestToolContractSnapshot` freezes the tool *schemas* — names, descriptions, annotations,
input/output schemas — as a sha256 constant. It covers no response body, and on drift
reports `got <hash>, want <hash>` with no diff, so updating it is a blind replacement.

The assertion covering the failed-operation path is
`strings.Contains(failure.Error(), "retry_ref=")`. It is satisfied by a response carrying no
structured content at all, which is what that path returns.

## 2. What the fixture is

`writeFixtureGraph` writes two entries and one attachment: a tactical gap signal (~10 lines,
participant `Tester`), a capture procedure fixture (~88 lines, `canonical: capture`) that
**supersedes the embedded capture procedure**, and `notes.md` containing `0123456789`.

The fixture procedure is not the shipped one: no `guide`/`guideReview` steps, `assemble →
playback` directly, `newEntry` hung off `write` rather than a separate `publish`, four
instruction units authored inline (`"Draft the entry. Existing topics: {{.viewLayout}}"`).

Response sizes against it:

| response | total | framing | instructions | schema |
| --- | --- | --- | --- | --- |
| `start_session` | 16,020 B | 5,753 | 9,375 | 442 |
| `start_procedure` (capture) | 2,569 B | 0 (deduped) | 405 | 1,838 |

The 15 KB of prose in `start_session` is bundled content — the `user-dialogue` shell
orientation and base-fact framing — not fixture content, and lane dedup places it in one
response per connection. Equivalent responses driven against this repository's own graph
measured 33 KB, 47 KB and 59 KB; those figures describe the real graph and are not the basis
for a decision about snapshot tests.

## 3. The error surface as it stands

`pkg/application/errors.go` already carries the pattern: an `ErrorCode` string type with
fourteen values (`authentication_required`, `invalid_argument`, `project_required`,
`project_unavailable`, `action_required`, `read_denied`, `write_denied`,
`branch_unavailable`, `session_ownership_mismatch`, `session_conflict`, `session_ended`,
`graph_conflict`, `migration_required`, `recovery_required`) and an `ApplicationError` with
`Code`, `Message`, structured context, `Cause` and `Unwrap()`.

It is applied in patches. The engine's `OperationError` does not participate. There are 151
`fmt.Errorf` sites in `pkg/application`, 170 in `internal/engine`, and 19 `toolError` sites
in `pkg/mcpapp`.

`ApplicationError` holds its prose inline in `Message`, which is what `Error()` returns — so
code and text travel together and the text is written where the failure is raised. That is how
`next(instance=…, retry_ref=…)` came to be embedded in engine-produced text.

`20260829-113416-d-cpt-nut` classifies failures by who must act, and leaves open "whether the
three non-waiting cases ever need naming in code rather than only in the message".

## 4. Rejected alternatives

**Digesting or eliding prose in snapshot tests.** A digest still fails on every prose edit while
reporting only that a hash moved — the same defect as the contract hash. Eliding blinds the
snapshot to a refactor that should preserve output, which is the case a snapshot test exists
for. Verbatim is workable because regeneration is one command and the git diff is the review;
what makes it workable is rendering prose as text that diffs line by line rather than as one
JSON-escaped string.

**A stubbed application root behind an interface.** `pkg/mcpapp` calls 21 methods on
`*sdd.WorkflowSession` (`Abandon`, `Advance`, `BindBranch`, `Branch`, `EditStagedAttachment`,
`Finished`, `Framing`, `ID`, `IsShell`, `LogRead`, `OpenInstances`, `Park`, `Project`,
`ReadScope`, `ReadStagedAttachment`, `RecordServed`, `Reorient`, `ServeAll`, `ServedBefore`,
`StageAttachment`, `Start`). Faking that contract would freeze a hand-built idea of what the
application produces — and the defect being fixed is precisely that the delivered response did
not match what was assumed, including in the entry that decided it. The suite runs the real
stack in 12.4s, so the decoupling buys nothing that pays for the drift risk.

**Session-handle injection for determinism.** Rejected in favour of aliasing each distinct
handle in the snapshot renderer. `pkg/application` is public surface under
`20260830-114446-d-cpt-xc3`, so a generator field would make weakening the handle a supported
public option; `20260828-165352-d-cpt-aen` keeps unguessability as a premise even though the
handle composes with authentication rather than replacing it. Aliasing preserves the identity
relationships a scrub would destroy.

**Codes on all 321 `fmt.Errorf` sites.** Rejected as ceremony. Codes are assigned where the
condition is known, by wrapping the cause; a registry nobody reads is its own maintenance
surface.
