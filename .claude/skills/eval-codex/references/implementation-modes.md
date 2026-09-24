# Scenario cards: the implementation procedure's run modes

Each card runs on a repo from `driver.py setup` with the default task (a `farewell` function with a test). The procedure under test is the shipped implementation procedure; the standard every run is measured against is 20260923-185316-d-cpt-5gk, the mechanism 20260923-230855-d-cpt-34w with 20260923-233057-d-cpt-ekd.

**Opening message, all cards:** `$sdd Please implement the recorded farewell task.`

**Checks common to every tracked card** (all but quick):

- The marker exists on the branch the agent was on when the mode was answered — `main` in every card here — right after setup (`status`).
- The agent asked for the run mode and waited; it asked for no base branch, work branch or other report the work does not need.
- The closing done is recorded after the code is committed, on the branch the work is on, and cites the commits.
- The marker is removed right after the done, on that same branch; `main` shows the work as taken until the merge.
- Nobody asks for a "landed" confirmation. The merge happens only after the user's go-ahead, and after it `main` has the code and the done and no marker.
- Every declaration the agent makes follows a served instruction; it never reads or edits `.sdd/` directly.

## inPlace

- **Goal** — contained work straight on main.
- **The user's part** — mode: "Do it directly on main, it's small." Done playback: "Looks right." Evaluation offer: "No, finish."
- **Checks** — commits on `main`; the marker is written and removed on `main`; no branch binding declared; no merge step.

## branch

- **Goal** — isolation in the same directory; the served base follows the checkout.
- **The user's part** — mode: "Use a branch." Done playback: "Looks right." Merge: "Yes, merge it into main." Evaluation offer: "No, finish."
- **Checks** — the agent branches off after the marker commit, so the branch carries the marker; no binding is needed, because the framing names the switched branch as the base (a declaration anyway is noted, not failed); the done and the marker removal are commits on the branch; after the merge `main` is clean.

## worktree

- **Goal** — isolation in a separate directory, with a declared branch.
- **The user's part** — mode: "Use a worktree, keep main untouched." Done playback: "Looks right." Merge: "Yes, merge it into main." Evaluation offer: "No, finish."
- **Checks** — the worktree is created from `main` after the marker commit; the agent declares the worktree's branch before capturing anything; the done and the marker removal land in the worktree's branch; after the merge the agent returns to the main checkout and clears the binding; `main` is clean.

## quick

- **Goal** — too small to track.
- **The user's part** — mode: "Too small to track, just do it." Done playback: "Looks right." Evaluation offer: "No, finish."
- **Checks** — no marker at any point; the done is still recorded and cites the commit.

## abandon from a work branch

- **Goal** — dropping a run after the host entered a branch.
- **The user's part** — mode: "Use a branch." Once the agent has started on the branch, and before it records a done: "Actually, let's drop this. Abandon the run." If asked about the branch: "Leave it."
- **Checks** — the agent returns to `main` (and clears a declared binding) before the run is abandoned; the run ends without a done; the marker is gone from `main`; the agent records the user's word for dropping it (20260923-231440-s-tac-hz2 expects this to be missing — note what the log shows).
