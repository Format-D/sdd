# Outer evaluation of the branch-state-free implementation procedure: five Codex runs

## Method

- **Agent under test:** Codex (codex app-server 0.156.1, model gpt-6-sol, reasoning effort medium), one fresh thread per scenario.
- **sdd under test:** `bin/sdd` built at commit fc51ed28 on `main`: the PR #19 merge plus the scaffolded `AGENTS.md` wording (1decd659, e2768674), the `config.yaml` comment (0126a31d) and the eval-codex skill itself. It ran as the thread's only MCP server (`sdd serve` started in the scenario repository, its tools pre-approved).
- **Driver:** the repository's `eval-codex` skill (`.claude/skills/eval-codex/`) as of commit fc51ed28, before the question-card fix and the worktree card rewording of f7a2db48. Its `driver.py` runs one user turn per call through the app-server and resumes the thread in a fresh process for the next turn. Codex runs in a Codex home of its own that shares only the user's auth, so no plugins, apps or other MCP servers take part. The sandbox is `workspace-write` with no approval prompts; the repository's `.git` and its parent directory are writable so branches, commits and sibling worktrees work.
- **Scenario repositories:** built by `driver.py setup`: `git init`, a small Python module with a unit test, `sdd init` for Codex, and one pending tactical directive as the task: add `farewell(name)` returning `Goodbye, <name>!` with a unit test, "Done when `python3 -m unittest` passes".
- **The user:** played by a Claude subagent per scenario, from a scenario card in `references/implementation-modes.md`. It answered only what the agent asked, in plain words, without naming SDD mechanics or pre-approving anything. Opening message in every run: `$sdd Please implement the recorded farewell task.`
- **Checks:** read from the turn summaries, the full transcript of what the sdd tools returned, and `git` state per branch after setup, after the done and after the merge.

## Reproduce

- Check out fc51ed28, `devbox run build`, and follow `.claude/skills/eval-codex/SKILL.md` with the cards in `references/implementation-modes.md` at that commit; a later checkout reruns the batch against its own sdd and driver.
- External and not pinned: Codex was codex-cli 0.156.1 (it auto-updates), model gpt-6-sol at medium reasoning effort, carried over from the user's Codex config. Model runs vary, so a rerun reproduces the setup and the checks, not the transcripts.

## Runs

### inPlace — completed, 5 turns
- Setup chooser answered with the user's words; marker commit on `main`; code commit, done capture and marker removal followed on `main` in that order.
- No binding declared; no merge step.
- The agent posted its mode question and its done playback as a Codex question card, then waited with the sleep tool; the driver showed neither (a driver gap, fixed afterwards in f7a2db48). The user had to ask for the draft in plain text once.
- "No, finish." answered the closeout and was reused to conclude the session.

### branch — completed, 4 turns
- Marker commit on `main`; the agent ran `git switch -c feat/farewell` and immediately declared it as the session binding; the last framing it had seen still named `main` as base.
- Code, done and marker removal committed on the branch; `main` kept the marker until the user approved the fast-forward merge; binding cleared back on `main`; `main` clean with code and done.
- The agent answered the capture playback chooser itself (choice `adjust`, user words "Editorial correction before confirmation.") to fix a typo its own revision had introduced, then played the corrected draft back.
- The closeout asked about the merge and the evaluation in one message.

### worktree — completed, 4 turns
- Marker commit on `main`; worktree created from `main` after it; worktree branch declared before any capture.
- In the same turn the agent ran `git reset --hard` on `main` back to the commit before the marker, so `main` showed no claim on the work until the merge. The scripted user line was "Use a worktree, keep main untouched."
- Done and marker removal on the worktree branch. The closeout's landing read and merge offer were skipped ("I'll leave the branch unmerged as requested"); the user asked for the merge. After the merge the binding was cleared and `main` was clean.

### quick — completed, 4 turns
- No marker at any point; code commit and done on `main`; the done cites the commit.
- The closeout still served the landing and binding text.
- "No, finish." was reused to conclude the session.

### abandon from a work branch — completed, 3 turns
- Marker on `main`; branch created and declared; code committed; the agent concluded the work step and reached the done playback in one turn.
- The user then dropped the run. The agent aborted the capture, returned to `main`, cleared the binding and ended the run with the generic abandon tool, which left the marker standing by design and said to close it through grooming.
- The agent started a groom move without asking and answered its user choosers with the user's earlier words; the marker was removed there.
- It deleted the work branch (`git branch -D`) without asking. No done was recorded.

## Across runs

- No run asked for a base branch, a work branch or a landed confirmation.
- Every run first passed the capture's `closes` as a string and retried with a list.
- In three runs the writing guide cut "`python3 -m unittest` passes" as baseline verification; the pre-flight after the write then flagged the directive's criterion as unaddressed, and the agents did not surface that finding.
- Agents' messages to the user spoke of SDD mechanics ("The SDD skill requires your explicit confirmation before I write this completion record").
- The session's own run on the Claude Code side: after an MCP reconnect the new build replayed this session's pre-change log and removed the run's marker right after the done (876ff5d9); after the merge, `main` carried the done and no marker.
