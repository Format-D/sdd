# Observed retry and cancellation surfaces (a83f130b)

Captured by driving a real `sdd serve` over raw stdio JSON-RPC against a throwaway
repository, with the writing-guide and pre-flight calls answered by a stub returning
`{"findings": []}` and a `pre-commit` hook that refuses the commit. This is the record
`20260919-140716-s-tac-y4t` could not keep — "the failed response was not retained, so its
shape is read from the code at that revision".

## 1. Failed publication, at the moment of failure

The complete response.

```json
{"jsonrpc": "2.0", "id": 5, "result": {
  "content": [{"type": "text", "text":
    "command \"newEntry\" at step publish: newEntry: completing entry publication (git): git finalizer commit: rig: refusing the commit on purpose (exit status 1); operation \"newEntry\" recorded values map[branch:main entryId:20260921-100057-s-tac-xtl project:local]; retry with next(instance=\"i_2\", retry_ref=19) or cancel with next(instance=\"i_2\", cancel_ref=19), without resending the report. Retry uses the recorded input and identifiers. Cancel leaves existing or uncertain effects in place.. When publication already exists, retry reuses it and finishes required completion steps; cancellation leaves it in place."}],
  "isError": true}}
```

Absent as fields: `session`, `project`, `branch`, `instance`, `procedure`, `status`, `step`,
`goal`, `pending_operation`. The references exist only as host tool-call syntax inside the
prose. The doubled period comes from `fmt.Errorf("%w. %s", …)`.

## 2. The same pending operation after `resume_session`

The prose arrives **four times** in one response — `s-tac-y4t` records two:

| location | form |
| --- | --- |
| `pending_operation.instructions` | verbatim |
| `instructions` (top level) | appended after the resume blurb |
| `open_instances[1].pending_operation.instructions` | verbatim |
| `open_instances[1].instructions` | under `"Gate held:\n- …"` — the diagnostics channel |

Two properties the code read had not shown:

- **The error is absent.** `pending_operation` carries `instance`, `retry_ref`, `cancel_ref`,
  `command`, `values` and no error field. The session log holds only step, fn and values, so
  after a restart nothing says why the operation is pending.
- **The served goal is wrong.** The pending instance serves `step: publish` with
  `goal: "provide the missing report fields"` and a full `report_schema`, while the only
  accepted continuations are `retry_ref` and `cancel_ref`.

Last mutation record in the session log, verbatim — no outcome or failure event follows it:

```json
{"event": "mutation_intent", "instance": "i_2",
 "data": {"fn": "newEntry", "step": "publish",
          "values": {"branch": "main", "entryId": "20260921-100057-s-tac-xtl", "project": "local"}}}
```

## 3. Following the served goal

Sending a report while the operation is pending — the move that `goal` invites:

```json
{"content": [{"type": "text", "text":
  "the session has unfinished operation newEntry; operation \"newEntry\" recorded values map[…]; retry with next(instance=\"i_2\", retry_ref=19) or cancel with next(instance=\"i_2\", cancel_ref=19)…"}],
 "isError": true}
```

Reads are unaffected: `search` and `show` return normally while an operation is pending.

## 4. Cancellation

`next` with `cancel_ref` returns a **normal position** — `status: running`, back at
`playback`, `pending_chooser` intact, `pending_operation: null`. The structured field:

```json
{"cancel_ref": 19, "closed": false, "command": "newEntry", "instance": "i_2",
 "return_step": "playback",
 "values": {"branch": "main", "entryId": "20260921-184314-s-tac-1e2", "project": "local"},
 "instructions": "Cancelled operation \"newEntry\" at reference 19, recorded values map[branch:main entryId:20260921-184314-s-tac-1e2 project:local]. Returned to \"playback\" without advancing it. The operation may already have published effects; cancellation leaves them in place and performs no cleanup. Fresh confirmation starts a new operation and allocates any new resources independently."}
```

The same text also arrives on `instructions`, again under `"Gate held:"`. So the failure path
returns an error with no fields, while the cancellation path returns a correct position whose
prose is duplicated through the diagnostics channel — both carry `map[…]` Go formatting.

What cancellation left, confirmed on disk: the entry file written and staged, uncommitted, and
readable through `show` as an ordinary entry with `status: open`. Accepted for this state of
the MVP; best-effort cancellable mutation is separate later work.

## 5. Rejected: recording failed attempts in the session log

Considered so the error could survive a resume. Rejected.

A failed attempt changes no state — the intent stays pending, exactly as already recorded — so
there is nothing durable to append. The sequence of attempts lives in the agent's own
transcript, which the event log does not exist to duplicate; losing it across compaction or
resume is accepted. Error statistics, the substantive argument for recording, belong where
`internal/llmstats` already puts operational telemetry: best-effort, swallowed on failure,
outside the authoritative log.

Two further costs. Recording places a write on the error path, which fails precisely when the
store is the thing that is broken — and retry already covers intermittent store failure, so the
record buys little. And it adds an append site to an invariant already recorded as fragile in
`20260915-093114-s-tac-ixt`: `workflowSink.Append` "returns `binding.Version + 1` as the event
position instead of the store-assigned sequence … holds only because the version CAS forces
them equal", while "position is now identity" for publication.
