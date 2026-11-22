#!/usr/bin/env python3
"""Stand-in agent CLI for the tests: no model, no login, no network beyond 127.0.0.1.

It never calls a model. It speaks Claude Code's ``stream-json`` output:
  * one-shot: reads the prompt from stdin, prints text deltas and a ``result``;
  * warm (``--input-format stream-json``): one ``user`` line per turn on stdin,
    one ``result`` per turn, the process stays up;
  * a turn whose text contains ``TOOL:<name>`` calls that meeting tool through
    the MCP server named in ``--mcp-config`` and answers with its result;
  * ``auth status`` prints a login status.

Env: STUB_STARTUP_S (delay before init), STUB_FIRST_S / STUB_TOTAL_S (per turn),
STUB_AUTH_FAIL=1 (logged out).
"""

import json
import os
import re
import sys
import time
import urllib.request

ANSWER = "The answer is 42."
AUTH_ERROR = "Failed to authenticate: OAuth token has expired"


def emit(event):
    print(json.dumps(event), flush=True)


def arg_value(flag):
    if flag in sys.argv:
        idx = sys.argv.index(flag)
        if idx + 1 < len(sys.argv):
            return sys.argv[idx + 1]
    return None


def load_mcp_config():
    """``--mcp-config`` is a file path (the bridge keeps the secret out of argv) or inline JSON."""
    value = arg_value("--mcp-config") or "{}"
    if os.path.isfile(value):
        with open(value, encoding="utf-8") as handle:
            return json.load(handle)
    return json.loads(value)


def call_mcp_tool(name):
    config = load_mcp_config()
    server = next(iter((config.get("mcpServers") or {}).values()), None)
    if not server:
        return "no MCP server configured"

    def rpc(method, params, msg_id):
        req = urllib.request.Request(
            server["url"],
            method="POST",
            data=json.dumps({"jsonrpc": "2.0", "id": msg_id, "method": method, "params": params}).encode(),
            headers={"Content-Type": "application/json", **server.get("headers", {})},
        )
        with urllib.request.urlopen(req, timeout=70) as resp:
            return json.loads(resp.read())

    rpc("initialize", {"protocolVersion": "2025-06-18", "capabilities": {}}, 1)
    listed = [t["name"] for t in rpc("tools/list", {}, 2)["result"]["tools"]]
    if name not in listed:
        return f"tool {name} not listed"
    result = rpc("tools/call", {"name": name, "arguments": {"text": "hi"}}, 3)["result"]
    return result["content"][0]["text"]


def answer_turn(text):
    if os.environ.get("STUB_AUTH_FAIL") == "1":
        emit({"type": "result", "is_error": True, "result": AUTH_ERROR})
        return
    if os.environ.get("STUB_FAIL_TEXT"):  # e.g. a CLI out of credits
        emit({"type": "result", "is_error": True, "result": os.environ["STUB_FAIL_TEXT"]})
        return
    first = float(os.environ.get("STUB_FIRST_S", "0"))
    total = float(os.environ.get("STUB_TOTAL_S", "0"))
    answer = ANSWER
    tool = re.search(r"TOOL:([A-Za-z0-9_]+)", text)
    if tool:
        answer = "Tool said: " + call_mcp_tool(tool.group(1))
    elif "<meeting_turns>" not in text:
        answer = "no meeting turns"
    elif "LONGSTREAM" in text:
        # Like Claude Code seen live (11-17): a slow answer that keeps streaming
        # for a moment after SIGTERM, so a barge-in lands mid-output.
        import signal

        signal.signal(signal.SIGTERM, signal.SIG_IGN)
        answer = " ".join(f"stale{i}" for i in range(60))
    time.sleep(first)
    for word in answer.split(" "):
        event = {"type": "content_block_delta", "delta": {"type": "text_delta", "text": word + " "}}
        emit({"type": "stream_event", "event": event})
        if "LONGSTREAM" in text:
            time.sleep(0.03)
    time.sleep(max(0.0, total - first))
    result = {"type": "result", "is_error": False, "result": answer}
    if os.environ.get("STUB_USAGE") == "1":  # Claude Code's real field names
        model = arg_value("--model") or "default"
        result["usage"] = {
            "input_tokens": 10,
            "cache_creation_input_tokens": 9000,
            "cache_read_input_tokens": 0,
            "output_tokens": 10 * len(model),
            "output_tokens_details": {"thinking_tokens": 5},
        }
        result["total_cost_usd"] = 0.01 * len(model)
    emit(result)


def _rpc_loop(handle):
    """Line-delimited JSON-RPC on stdio (ACP and Codex app-server stand-ins)."""
    import threading

    pending = {}

    def reader():
        for line in sys.stdin:
            try:
                msg = json.loads(line)
            except ValueError:
                continue
            if "method" not in msg and msg.get("id") in pending:  # a reply to our request
                pending.pop(msg["id"]).put(msg)
                continue
            threading.Thread(target=handle, args=(msg, pending), daemon=True).start()

    reader()


def list_stdio_mcp_tools():
    """Like agents that load MCP tools at session start (STUB_MCP_AT_START=1)."""
    import subprocess

    server = next(iter(_acp.mcp), None)
    if not server:
        return
    env = dict(os.environ)
    env.update({item["name"]: item["value"] for item in server.get("env") or []})
    proc = subprocess.Popen(
        [server["command"], *server.get("args", [])],
        stdin=subprocess.PIPE,
        stdout=subprocess.PIPE,
        text=True,
        env=env,
    )
    for msg_id, method in ((1, "initialize"), (2, "tools/list")):
        proc.stdin.write(json.dumps({"jsonrpc": "2.0", "id": msg_id, "method": method, "params": {}}) + "\n")
        proc.stdin.flush()
        proc.stdout.readline()
    proc.stdin.close()
    proc.wait(timeout=10)


def call_stdio_mcp_tool(name):
    """Like an ACP agent: start the stdio MCP server from session/new, then list and call a tool."""
    import subprocess

    server = next(iter(_acp.mcp), None)
    if not server:
        return "no MCP server configured"
    env = dict(os.environ)
    env.update({item["name"]: item["value"] for item in server.get("env") or []})
    proc = subprocess.Popen(
        [server["command"], *server.get("args", [])],
        stdin=subprocess.PIPE,
        stdout=subprocess.PIPE,
        text=True,
        env=env,
    )

    def rpc(method, params, msg_id):
        proc.stdin.write(
            json.dumps({"jsonrpc": "2.0", "id": msg_id, "method": method, "params": params}) + "\n"
        )
        proc.stdin.flush()
        return json.loads(proc.stdout.readline())

    try:
        rpc("initialize", {"protocolVersion": "2025-06-18", "capabilities": {}}, 1)
        listed = [t["name"] for t in rpc("tools/list", {}, 2)["result"]["tools"]]
        if name not in listed:
            return f"tool {name} not listed"
        result = rpc("tools/call", {"name": name, "arguments": {"text": "hi"}}, 3)["result"]
        return result["content"][0]["text"]
    finally:
        proc.stdin.close()
        proc.wait(timeout=10)


def _acp(msg, pending):
    """Minimal ACP agent: streams words; 'SLOW' waits for session/cancel;
    'PERMISSION' asks for an execute permission and reports the outcome;
    'TOOL:<name>' calls that meeting tool through the stdio MCP server from session/new;
    'MODEL?' answers with the model set by session/set_model."""
    import queue

    method, mid, params = msg.get("method"), msg.get("id"), msg.get("params") or {}
    if method == "initialize":
        emit({"jsonrpc": "2.0", "id": mid, "result": {"protocolVersion": 1, "agentCapabilities": {}}})
    elif method == "session/new":
        if os.environ.get("STUB_AUTH_FAIL") == "1":
            emit(
                {"jsonrpc": "2.0", "id": mid, "error": {"code": -32000, "message": "Authentication required"}}
            )
            return
        _acp.mcp = params.get("mcpServers") or []
        if os.environ.get("STUB_MCP_AT_START") == "1":
            list_stdio_mcp_tools()
        created = {"sessionId": "sess_1"}
        if os.environ.get("STUB_ACP_MODELS") == "1":  # the shape `agy acp` returns (2026-11-18)
            created["models"] = {
                "availableModels": [
                    {"modelId": "flash-low", "name": "Flash (Low)"},
                    {"modelId": "flash-high", "name": "Flash (High)"},
                ],
                "currentModelId": "flash-high",
            }
            created["configOptions"] = [
                {
                    "id": "thought_level",
                    "category": "thought_level",
                    "type": "select",
                    "currentValue": "high",
                    "options": [{"value": v, "name": v.title()} for v in ("low", "medium", "high")],
                }
            ]
        emit({"jsonrpc": "2.0", "id": mid, "result": created})
    elif method == "session/set_config_option":
        _acp.effort = params.get("value")
        emit({"jsonrpc": "2.0", "id": mid, "result": {}})
    elif method == "session/set_model":
        _acp.model = params.get("modelId")
        emit({"jsonrpc": "2.0", "id": mid, "result": {}})
    elif method == "session/cancel":
        _acp.cancelled = True
    elif method == "session/prompt":
        text = "".join(p.get("text", "") for p in params.get("prompt") or [])
        answer = ANSWER
        tool = re.search(r"TOOL:([A-Za-z0-9_]+)", text) or re.search(
            r"[Cc]all the ([A-Za-z0-9_]+) tool", text
        )
        if os.environ.get("STUB_NO_TOOLS") == "1":
            tool = None
        if tool:
            answer = "Tool said: " + call_stdio_mcp_tool(tool.group(1))
        if "MODEL?" in text:
            answer = f"model {_acp.model}"
        if "EFFORT?" in text:
            answer = f"effort {_acp.effort}"
        if "PERMISSION" in text:
            ask_id = 9001
            box = queue.Queue()
            pending[ask_id] = box
            options = [
                {"optionId": "yes", "name": "Allow", "kind": "allow_once"},
                {"optionId": "no", "name": "Deny", "kind": "reject_once"},
            ]
            params = {
                "sessionId": "sess_1",
                "toolCall": {"toolCallId": "t1", "kind": "execute", "title": "rm -rf /"},
                "options": options,
            }
            if "NAMED_PERMISSION" in text:  # options told apart only by their names
                params["options"] = [
                    {"optionId": "a1", "name": "Allow", "kind": "other"},
                    {"optionId": "d1", "name": "Deny", "kind": "other"},
                ]
            if "BAD_PERMISSION" in text:  # malformed: the bridge must still answer
                params = {"sessionId": "sess_1", "toolCall": "not an object", "options": ["?", 3]}
            emit({"jsonrpc": "2.0", "id": ask_id, "method": "session/request_permission", "params": params})
            reply = box.get(timeout=10)
            if "error" in reply:
                answer = "permission error"
            else:
                outcome = reply["result"]["outcome"]
                answer = f"permission {outcome.get('optionId') or outcome.get('outcome')}"
        hold = re.search(r"WAIT_FOR:(\S+)", text)
        if hold:  # a turn the test ends on cue (no clock involved): wait for the file, or a cancel
            _acp.cancelled = False
            deadline = time.time() + 60
            while not os.path.exists(hold.group(1)) and time.time() < deadline:
                if _acp.cancelled:
                    emit({"jsonrpc": "2.0", "id": mid, "result": {"stopReason": "cancelled"}})
                    return
                time.sleep(0.02)
        if "SLOW" in text:
            _acp.cancelled = False
            for _ in range(500):
                if _acp.cancelled:
                    emit({"jsonrpc": "2.0", "id": mid, "result": {"stopReason": "cancelled"}})
                    return
                time.sleep(0.01)
        for word in answer.split(" "):
            emit(
                {
                    "jsonrpc": "2.0",
                    "method": "session/update",
                    "params": {
                        "sessionId": "sess_1",
                        "update": {
                            "sessionUpdate": "agent_message_chunk",
                            "content": {"type": "text", "text": word + " "},
                        },
                    },
                }
            )
        emit({"jsonrpc": "2.0", "id": mid, "result": {"stopReason": "end_turn"}})


_acp.cancelled = False
_acp.mcp = []
_acp.model = None
_acp.effort = None


def _app_server(msg, pending):
    """Minimal Codex app-server: threads, turns, deltas, interrupt."""
    method, mid, params = msg.get("method"), msg.get("id"), msg.get("params") or {}
    if method == "initialize":
        emit({"id": mid, "result": {"userAgent": "stub"}})
    elif method == "thread/start":
        _app_server.thread = params
        emit({"id": mid, "result": {"thread": {"id": "thr_1"}, "model": params.get("model") or "stub"}})
    elif method == "turn/interrupt":
        _app_server.interrupted.add(params.get("turnId"))
        emit({"id": mid, "result": {}})
    elif method == "turn/start":
        _app_server.n += 1
        turn_id = f"turn_{_app_server.n}"
        emit({"id": mid, "result": {"turn": {"id": turn_id, "status": "inProgress"}}})
        text = "".join(p.get("text", "") for p in params.get("input") or [])
        if os.environ.get("STUB_AUTH_FAIL") == "1":
            emit(
                {
                    "method": "error",
                    "params": {
                        "error": {"message": "401", "codexErrorInfo": "unauthorized"},
                        "willRetry": False,
                        "threadId": "thr_1",
                        "turnId": turn_id,
                    },
                }
            )
            return
        if "SLOW" in text:
            for _ in range(500):
                if turn_id in _app_server.interrupted:
                    emit(
                        {
                            "method": "turn/completed",
                            "params": {"threadId": "thr_1", "turn": {"id": turn_id, "status": "interrupted"}},
                        }
                    )
                    return
                time.sleep(0.01)
        for word in ANSWER.split(" "):
            emit(
                {
                    "method": "item/agentMessage/delta",
                    "params": {"threadId": "thr_1", "turnId": turn_id, "itemId": "i1", "delta": word + " "},
                }
            )
        emit(
            {
                "method": "turn/completed",
                "params": {"threadId": "thr_1", "turn": {"id": turn_id, "status": "completed"}},
            }
        )


_app_server.n = 0
_app_server.thread = {}
_app_server.interrupted = set()


def main():
    if sys.argv[1:2] == ["acp"]:
        _rpc_loop(_acp)
        return 0
    if sys.argv[1:2] == ["app-server"]:
        _rpc_loop(_app_server)
        return 0
    if sys.argv[1:3] == ["auth", "status"]:
        time.sleep(float(os.environ.get("STUB_AUTH_STATUS_S", "0")))  # a slow login check (CI runners)
        if os.environ.get("STUB_AUTH_FAIL") == "1":
            print("Not logged in · Please run /login")
            return 1
        print(json.dumps({"loggedIn": True}))
        return 0
    time.sleep(float(os.environ.get("STUB_STARTUP_S", "0")))
    emit({"type": "system", "subtype": "init"})
    if arg_value("--input-format") == "stream-json":
        for line in sys.stdin:
            try:
                msg = json.loads(line)
            except ValueError:
                continue
            answer_turn(str((msg.get("message") or {}).get("content") or ""))
        return 0
    answer_turn(sys.stdin.read())
    return 1 if os.environ.get("STUB_AUTH_FAIL") == "1" else 0


if __name__ == "__main__":
    sys.exit(main())
