#!/usr/bin/env python3
"""Drive a real Codex agent through `codex app-server` against a scratch SDD
repository, one user turn per invocation.

  driver.py setup REPO [--task TEXT]      scratch repo: git, sdd init, anchor directive, code
  driver.py start REPO MESSAGE [opts]     new Codex thread, first user turn
  driver.py say REPO MESSAGE              next user turn on the same thread
  driver.py log REPO [--turn N] [--max N] what the sdd tools served, in full
  driver.py status REPO                   Git state: branches, worktrees, WIP markers

State lives beside the repo in REPO.eval/, never inside it: thread id,
transcripts, app-server stderr, and a Codex home of its own (CODEX_HOME) that
shares only the user's auth.json, by symlink, and model settings, so the user's
Codex config, plugins, trust list and thread history stay out of the run. Every
turn starts a fresh app-server that resumes the thread, so a driving agent can
play the user between invocations.
"""
import argparse
import itertools
import json
import os
import queue
import random
import string
import subprocess
import sys
import threading
import time
import tomllib
from datetime import datetime
from pathlib import Path

ROOT = Path(__file__).resolve().parents[3]
DEFAULT_SDD = ROOT / "bin" / "sdd"


class AppServer:
    """Newline-delimited JSON-RPC client for `codex app-server` over stdio."""

    def __init__(self, repo, overrides, transcript, stderr, home):
        args = ["codex", "app-server"]
        for override in overrides:
            args += ["-c", override]
        self.log = open(transcript, "a", buffering=1)
        self.proc = subprocess.Popen(args, cwd=repo, stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                                     stderr=open(stderr, "a"), text=True, bufsize=1,
                                     env={**os.environ, "CODEX_HOME": str(home)})
        self.ids = itertools.count(1)
        self.inbox = queue.Queue()
        self.responses = {}
        threading.Thread(target=self._read, daemon=True).start()

    def _record(self, direction, msg):
        self.log.write(json.dumps({"ts": time.time(), "dir": direction, "msg": msg}) + "\n")

    def _read(self):
        for line in self.proc.stdout:
            if line.strip():
                msg = json.loads(line)
                self._record("in", msg)
                self.inbox.put(msg)
        self.inbox.put(None)

    def _send(self, msg):
        self._record("out", msg)
        self.proc.stdin.write(json.dumps(msg) + "\n")
        self.proc.stdin.flush()

    def _answer(self, msg):
        # Runs are unattended: every approval the sandbox still asks for is granted.
        method, params = msg["method"], msg.get("params", {})
        if method in ("item/commandExecution/requestApproval", "item/fileChange/requestApproval"):
            result = {"decision": "accept"}
        elif method == "mcpServer/elicitation/request":
            result = {"action": "accept", "content": {}, "_meta": None}
        elif method == "item/permissions/requestApproval":
            result = {"permissions": params.get("permissions", {}), "scope": "turn"}
        elif method == "item/tool/requestUserInput":
            result = {"answers": {}}
        else:
            self._send({"id": msg["id"], "error": {"code": -32601, "message": f"driver does not handle {method}"}})
            return
        self._send({"id": msg["id"], "result": result})

    def pump(self, timeout):
        msg = self.inbox.get(timeout=timeout)
        if msg is None:
            raise EOFError("codex app-server exited; see the stderr log in the state directory")
        if "method" in msg and "id" in msg:
            self._answer(msg)
        elif "id" in msg:
            self.responses[msg["id"]] = msg
        return msg

    def request(self, method, params, timeout=300):
        rid = next(self.ids)
        self._send({"method": method, "id": rid, "params": params})
        deadline = time.time() + timeout
        while rid not in self.responses:
            self.pump(max(0.1, deadline - time.time()))
        response = self.responses.pop(rid)
        if "error" in response:
            raise RuntimeError(f"{method}: {response['error']}")
        return response["result"]

    def initialize(self):
        self.request("initialize", {"clientInfo": {"name": "sdd_eval_codex", "title": "SDD eval-codex", "version": "1"},
                                    "capabilities": {"experimentalApi": True}})
        self._send({"method": "initialized"})

    def turn(self, thread_id, text, timeout, sandbox_policy=None):
        params = {"threadId": thread_id, "input": [{"type": "text", "text": text}]}
        if sandbox_policy:
            params["sandboxPolicy"] = sandbox_policy
        turn_id = self.request("turn/start", params)["turn"]["id"]
        items, deadline = [], time.time() + timeout
        while True:
            msg = self.pump(max(0.1, deadline - time.time()))
            method, params = msg.get("method"), msg.get("params", {})
            if method == "item/completed" and params.get("turnId") == turn_id:
                items.append(params["item"])
            elif method == "turn/completed" and params["turn"]["id"] == turn_id:
                return params["turn"], items

    def close(self):
        self.proc.stdin.close()
        try:
            self.proc.wait(timeout=10)
        except subprocess.TimeoutExpired:
            self.proc.terminate()


def state_dir(repo):
    path = Path(str(repo) + ".eval")
    path.mkdir(exist_ok=True)
    return path


def load_state(repo):
    path = state_dir(repo) / "state.json"
    if not path.exists():
        sys.exit(f"no thread for {repo}; run `start` first")
    return json.loads(path.read_text())


def save_state(repo, state):
    (state_dir(repo) / "state.json").write_text(json.dumps(state, indent=2))


USER_CODEX_HOME = Path(os.environ.get("CODEX_HOME", Path.home() / ".codex"))
CARRIED_SETTINGS = ("model", "model_reasoning_effort", "service_tier")
# sleep_tool off: Codex asks by posting a question and sleeping until a UI
# answers inside the turn; without it, the question ends the turn as in a chat.
DISABLED_FEATURES = ("plugins", "apps", "computer_use", "browser_use", "in_app_browser", "image_generation", "memories",
                     "sleep_tool")


def codex_home(repo):
    """The run's own Codex home, created on first use."""
    home = state_dir(repo) / "codex-home"
    auth = home / "auth.json"
    if home.exists():
        if not auth.is_symlink():
            # Codex replaced the link on a token refresh; the user's own copy may now be stale.
            print(f"warning: {auth} is no longer a symlink to {USER_CODEX_HOME / 'auth.json'}", file=sys.stderr)
        return home
    home.mkdir()
    auth.symlink_to(USER_CODEX_HOME / "auth.json")
    user_config = USER_CODEX_HOME / "config.toml"
    user = tomllib.loads(user_config.read_text()) if user_config.exists() else {}
    lines = ["notify = []"] + [f"{key} = {json.dumps(user[key])}" for key in CARRIED_SETTINGS if key in user]
    lines += ["", "[features]"] + [f"{name} = false" for name in DISABLED_FEATURES]
    (home / "config.toml").write_text("\n".join(lines) + "\n")
    return home


def launch_overrides(repo, sdd):
    """This checkout's sdd binary as the run's only MCP server."""
    return [
        f"mcp_servers.sdd.command={json.dumps(str(sdd))}",
        'mcp_servers.sdd.args=["serve"]',
        f"mcp_servers.sdd.cwd={json.dumps(str(repo))}",
        'mcp_servers.sdd.default_tools_approval_mode="approve"',
    ]


def thread_params(repo, state):
    params = {"cwd": str(repo), "sandbox": state["sandbox"], "approvalPolicy": "never", "approvalsReviewer": "user"}
    if state.get("model"):
        params["model"] = state["model"]
    if state.get("effort"):
        params["config"] = {"model_reasoning_effort": state["effort"]}
    return params


def sandbox_policy(repo, state):
    """workspace-write keeps .git read-only and confines writes to the repo;
    Git work and sibling worktrees need .git and the repo's parent writable."""
    if state["sandbox"] != "workspace-write":
        return None
    return {"type": "workspaceWrite", "writableRoots": [str(repo / ".git"), str(repo.parent)],
            "networkAccess": False, "excludeTmpdirEnvVar": False, "excludeSlashTmp": False}


def served(result):
    """The sdd tool's result as a dict, when it returned JSON."""
    if not result:
        return None
    if isinstance(result.get("structuredContent"), dict):
        return result["structuredContent"]
    for part in result.get("content") or []:
        if part.get("type") == "text":
            try:
                return json.loads(part["text"])
            except (ValueError, TypeError):
                return None
    return None


def brief(value, limit=160):
    text = value if isinstance(value, str) else json.dumps(value, ensure_ascii=False)
    return text if len(text) <= limit else text[:limit] + "…"


def print_turn(number, turn, items):
    print(f"== turn {number}: {turn['status']} in {turn.get('durationMs', 0) / 1000:.0f}s")
    for item in items:
        kind = item["type"]
        if kind == "mcpToolCall":
            serve = served(item.get("result")) or {}
            position = " ".join(f"{k}={serve[k]}" for k in ("procedure", "step", "status") if serve.get(k))
            missing = f" missing={serve['missing']}" if serve.get("missing") else ""
            error = f" ERROR {brief(item['error'])}" if item.get("error") else ""
            print(f"  [{item['server']}] {item['tool']} {brief(item.get('arguments'))} -> {position}{missing}{error}")
        elif kind == "commandExecution":
            print(f"  [sh] {brief(item['command'], 200)} (exit {item.get('exitCode')})")
        elif kind == "fileChange":
            for change in item.get("changes", []):
                print(f"  [edit] {change.get('path')} ({change.get('kind')})")
    if turn.get("error"):
        print(f"  turn error: {brief(turn['error'], 400)}")
    messages = [i for i in items if i["type"] == "agentMessage"]
    final = [m for m in messages if m.get("phase") == "final_answer"] or messages[-1:]
    print("== agent says:")
    for message in final:
        print(message["text"])
        for question in message.get("questions") or []:
            print(f"  [question] {question.get('title')}")
            for option in question.get("options") or []:
                print(f"    - {option}")
    if not final:
        print("(no message)")


def run(repo, state, text, timeout, fresh):
    directory = state_dir(repo)
    server = AppServer(repo, launch_overrides(repo, Path(state["sdd"])),
                       directory / "transcript.jsonl", directory / "appserver.stderr.log", codex_home(repo))
    try:
        server.initialize()
        params = thread_params(repo, state)
        if fresh:
            state["thread"] = server.request("thread/start", params)["thread"]["id"]
        else:
            server.request("thread/resume", {"threadId": state["thread"], **params})
        state["turns"] = state.get("turns", 0) + 1
        save_state(repo, state)
        if fresh:
            status = server.request("mcpServerStatus/list", {"threadId": state["thread"], "detail": "toolsAndAuthOnly"})
            live = sorted(s["name"] for s in status["data"] if s.get("runtimeStatus") != "disabled")
            print("== live MCP servers:", ", ".join(live))
        turn, items = server.turn(state["thread"], text, timeout, sandbox_policy(repo, state))
        with open(directory / "turns.jsonl", "a") as log:
            log.write(json.dumps({"turn": state["turns"], "user": text, "status": turn["status"], "items": items}) + "\n")
        print_turn(state["turns"], turn, items)
    finally:
        server.close()


def git(repo, *args, check=True):
    result = subprocess.run(["git", "-C", str(repo), *args], capture_output=True, text=True)
    if check and result.returncode != 0:
        sys.exit(f"git {' '.join(args)}: {result.stderr.strip()}")
    return result.stdout.strip()


ANCHOR = """---
type: decision
kind: directive
layer: tactical
intent: pending
confidence: high
participants:
    - Eval User
summary: {summary}
---

{body}
"""


def cmd_setup(args):
    repo = Path(args.repo).resolve()
    if repo.exists():
        sys.exit(f"{repo} exists; setup builds a fresh repository")
    repo.mkdir(parents=True)
    git(repo, "init", "-q", "-b", "main")
    for key, value in (("user.name", "Eval User"), ("user.email", "eval@example.invalid"), ("commit.gpgsign", "false")):
        git(repo, "config", key, value)
    (repo / "greeting.py").write_text('def greet(name):\n    return f"Hello, {name}!"\n')
    (repo / "test_greeting.py").write_text(
        "import unittest\n\nfrom greeting import greet\n\n\n"
        "class GreetTest(unittest.TestCase):\n    def test_greet(self):\n"
        '        self.assertEqual(greet("Ada"), "Hello, Ada!")\n\n\n'
        'if __name__ == "__main__":\n    unittest.main()\n')
    git(repo, "add", ".")
    git(repo, "commit", "-q", "-m", "Add greeting module")
    subprocess.run([str(Path(args.sdd).resolve()), "init", "--scope", "project", "--agents", "codex",
                    "--graph-dir", ".sdd/graph", "--participant", "Eval User", "--language", "en"],
                   cwd=repo, stdin=subprocess.DEVNULL, check=True, capture_output=True)
    config = repo / ".sdd" / "config.yaml"
    config.write_text(config.read_text().replace("# repo_id: github.com/org/repo", f"repo_id: eval.invalid/{repo.name}"))
    now = datetime.now()
    suffix = "".join(random.choices(string.ascii_lowercase + string.digits, k=3))
    anchor = f"{now:%Y%m%d-%H%M%S}-d-tac-{suffix}"
    entry = repo / ".sdd" / "graph" / f"{now:%Y}" / f"{now:%m}" / f"{now:%d-%H%M%S}-d-tac-{suffix}.md"
    entry.parent.mkdir(parents=True, exist_ok=True)
    entry.write_text(ANCHOR.format(summary=args.summary, body=args.task))
    git(repo, "add", ".")
    git(repo, "commit", "-q", "-m", "Record the task as a directive")
    print(f"repo: {repo}\nanchor: {anchor}")


def cmd_start(args):
    repo = Path(args.repo).resolve()
    state = {"sdd": str(Path(args.sdd).resolve()), "model": args.model, "effort": args.effort, "sandbox": args.sandbox}
    run(repo, state, args.message, args.timeout, fresh=True)


def cmd_say(args):
    repo = Path(args.repo).resolve()
    run(repo, load_state(repo), args.message, args.timeout, fresh=False)


def cmd_log(args):
    repo = Path(args.repo).resolve()
    path = state_dir(repo) / "turns.jsonl"
    for line in path.read_text().splitlines():
        record = json.loads(line)
        if args.turn and record["turn"] != args.turn:
            continue
        print(f"==== turn {record['turn']} — user: {record['user']}")
        for item in record["items"]:
            if item["type"] != "mcpToolCall":
                continue
            print(f"---- [{item['server']}] {item['tool']} {json.dumps(item.get('arguments'), ensure_ascii=False)}")
            serve = served(item.get("result"))
            text = json.dumps(serve, indent=1, ensure_ascii=False) if serve else brief(item.get("result") or item.get("error"), 100000)
            print(text if len(text) <= args.max else text[:args.max] + f"\n… ({len(text)} chars)")


def cmd_status(args):
    repo = Path(args.repo).resolve()
    print("-- branches")
    print(git(repo, "branch", "-a", "-vv"))
    print("-- worktrees")
    print(git(repo, "worktree", "list"))
    print("-- recent history (all branches)")
    print(git(repo, "log", "--all", "--graph", "--oneline", "-n", "25"))
    print("-- WIP markers per branch")
    for branch in git(repo, "for-each-ref", "--format=%(refname:short)", "refs/heads").splitlines():
        markers = git(repo, "ls-tree", "--name-only", branch, ".sdd/graph/wip/", check=False)
        print(f"  {branch}: {', '.join(Path(m).name for m in markers.splitlines()) or 'none'}")
    print("-- uncommitted")
    print(git(repo, "status", "--short") or "  clean")


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    commands = parser.add_subparsers(dest="command", required=True)

    setup = commands.add_parser("setup")
    setup.add_argument("repo")
    setup.add_argument("--sdd", default=str(DEFAULT_SDD))
    setup.add_argument("--summary", default="Add a farewell function to the greeting module.")
    setup.add_argument("--task", default=(
        "Add a `farewell(name)` function to `greeting.py` that returns `Goodbye, <name>!`, "
        "with a unit test in `test_greeting.py`. Done when `python3 -m unittest` passes."))
    setup.set_defaults(func=cmd_setup)

    start = commands.add_parser("start")
    start.add_argument("repo")
    start.add_argument("message")
    start.add_argument("--sdd", default=str(DEFAULT_SDD))
    start.add_argument("--model")
    start.add_argument("--effort")
    start.add_argument("--sandbox", default="workspace-write", choices=["read-only", "workspace-write", "danger-full-access"])
    start.add_argument("--timeout", type=int, default=1800)
    start.set_defaults(func=cmd_start)

    say = commands.add_parser("say")
    say.add_argument("repo")
    say.add_argument("message")
    say.add_argument("--timeout", type=int, default=1800)
    say.set_defaults(func=cmd_say)

    log = commands.add_parser("log")
    log.add_argument("repo")
    log.add_argument("--turn", type=int)
    log.add_argument("--max", type=int, default=6000)
    log.set_defaults(func=cmd_log)

    status = commands.add_parser("status")
    status.add_argument("repo")
    status.set_defaults(func=cmd_status)

    args = parser.parse_args()
    args.func(args)


if __name__ == "__main__":
    main()
