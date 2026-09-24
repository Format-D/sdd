---
name: eval-codex
description: Evaluate an SDD flow in real agent use by driving a Codex agent through `codex app-server` against a scratch repository, playing the user turn by turn, and checking what the sdd tools served against what the agent did. Invoke when a change to procedures, served instructions or the MCP surface needs validation beyond tests, or when the user asks for a Codex evaluation run.
---

# Evaluate an SDD flow with a real Codex agent

Tests prove the mechanism. This skill checks the experience: whether an agent that only reads what the sdd tools serve does the right work, in the right order, on the right branch, and whether the run demands anything the work does not need (20260923-185316-d-cpt-5gk). You play the user; Codex is the agent under test.

## Before a batch

- `codex login` done; `git` and Python 3.11+ on `PATH`.
- Build the sdd under test: `devbox run build` in the checkout whose code you evaluate. The driver uses that checkout's `bin/sdd` when it lives in it; pass `--sdd <path>` to `setup` and `start` otherwise.
- Pick a batch directory in your scratchpad, never inside the sdd repository. Each scenario gets its own subdirectory, `<batch>/<scenario>/repo`, because the sandbox makes the repo's parent writable for sibling worktrees.

## The driver

`driver.py` next to this file, stdlib Python:

```
python3 driver.py setup  <repo> [--task TEXT] [--summary TEXT]   fresh repo: git, sdd init for Codex, a code module, the task as a pending directive
python3 driver.py start  <repo> "<first user message>" [--model M] [--effort E]
python3 driver.py say    <repo> "<next user message>"
python3 driver.py log    <repo> [--turn N] [--max CHARS]          everything the sdd tools returned, per call
python3 driver.py status <repo>                                   branches, worktrees, history, WIP markers per branch
```

`setup` prints the anchor directive's ID. `start` and `say` run one turn each and print the sdd tool calls with the served position (procedure, step, missing fields), the shell commands with exit codes, and the agent's final message. The full transcript lands in `<repo>.eval/`.

What the driver fixes for every run:

- **Own Codex home.** `<repo>.eval/codex-home` links your `~/.codex/auth.json` and carries your model settings, nothing else: no plugins, apps, other MCP servers or notify hook, and trust entries and threads stay out of your Codex config. If it warns that `auth.json` is no longer a symlink, Codex refreshed the token into the copy; log in again if your own Codex then complains.
- **Only this sdd.** The run's single MCP server is the chosen `sdd serve`, started in the repo, its tools pre-approved.
- **Sandbox.** `workspace-write` with no approval prompts; the repo's `.git` and its parent are writable so branches, commits and worktrees work.
- **Codex uses skills, not slash commands.** Start SDD work with `$sdd` in the message, the way a Codex user would.

## Playing the user

You are the user, not a coach. The run is only worth something if the agent gets its guidance from what sdd serves.

- Say what a user would say, in plain words, and only what the agent asked for. Give real decisions (which mode, whether the draft is right, whether to merge) when the agent asks for them.
- Never name tools, steps, fields or SDD mechanics, never tell the agent how to proceed, and never pre-approve ("and confirm whatever comes").
- When the agent asks for something the scenario does not cover, answer as a reasonable user would and note it.
- When the agent asks for a report, a Git operation or a confirmation the work did not need, answer it and record it as a finding — that is exactly what the evaluation looks for.
- Stop after about 25 turns without progress, or when the agent is stuck on an error, and report where it stuck.

After each turn read the summary. Use `log` when you judge whether the agent followed what was served, and `status` at the moments that matter: after setup, after the marker should exist, after the done, after the merge.

## Scenario card

Give each run a card:

- **Goal** — what the run shows.
- **Opening message** — the first user message.
- **The user's part** — the decisions the user makes and their words for them.
- **Checks** — what must hold, as observable facts in `status`, `log` or the turn summaries.

The cards for the implementation procedure's run modes are in [references/implementation-modes.md](references/implementation-modes.md).

## Running a batch

Scenarios are independent: dispatch one subagent per card, in parallel. Give each subagent this skill's path, its card, the driver path, its scenario directory and the sdd binary, and have it return the report below. Keep the batch's results in the scratchpad.

## Report

Per scenario:

1. **Outcome** — completed, stuck (where), or aborted.
2. **Timeline** — one line per turn: what the user said, what the agent did (sdd calls, Git commands).
3. **Checks** — each check from the card, held or not, with the evidence: turn number, the served text, the Git state.
4. **Findings** — instructions that were missing, wrong or ignored; demands the work did not need; anything that surprised you. Quote the served text next to what the agent did.

Report what happened; do not fix code, specs or the scratch repo mid-run. What the findings mean is settled with the user afterwards.

Remove the batch directory once the findings are recorded.
