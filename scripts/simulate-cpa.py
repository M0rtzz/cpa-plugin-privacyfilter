#!/usr/bin/env python3
"""Exercise the built native plugin in a real CPA process against a local mock.

No real model provider or credentials are used. Artifacts contain only synthetic
requests. Run from any directory; see --help for build artifact paths.
"""
import argparse
import json
import os
from pathlib import Path
import shutil
import socket
import subprocess
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.error import HTTPError, URLError
from urllib.request import ProxyHandler, Request, build_opener

NOTICE = "本轮指令因汉字占比超过 20% 被拦截。为避免模型降智，请自行将指令翻译成英语后重新发送，并明确要求模型必须用英语回复。\n提示词示例：\nWrite all conversational replies in English, including explanations, questions that require my answer, and their answer options.\n\nWhen creating or editing files, Chinese may be used where appropriate, including documentation, code comments, text displayed in frontend pages, etc. Follow the language requirements of the task and the repository for those files."
OPENER = build_opener(ProxyHandler({}))


def message(text, kind="user.text"):
    return {"type": "message", "role": "user", "content": [{"type": "input_text", "text": text}],
            "internal_chat_message_metadata_passthrough": {
                "turn_id": "synthetic-turn", "content_item_kinds": [kind]}}


def free_port():
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


class Mock(BaseHTTPRequestHandler):
    calls = []

    def log_message(self, *_args):
        pass

    def do_POST(self):
        body = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        self.calls.append({"path": self.path, "body": body})
        text = "mock English reply"
        chat = {"id": "chatcmpl-mock", "object": "chat.completion", "created": 1,
                "model": "mock-model", "choices": [{"index": 0, "message": {
                    "role": "assistant", "content": text}, "finish_reason": "stop"}],
                "usage": {"prompt_tokens": 1, "completion_tokens": 3, "total_tokens": 4}}
        output = {"type": "message", "id": "msg_mock", "role": "assistant", "status": "completed",
                  "content": [{"type": "output_text", "text": text, "annotations": []}]}
        response = {"id": "resp_mock", "object": "response", "created_at": 1, "status": "completed",
                    "model": "mock-model", "output": [output], "usage": {"input_tokens": 1, "output_tokens": 3, "total_tokens": 4}}
        if body.get("stream"):
            self.send_response(200)
            self.send_header("Content-Type", "text/event-stream")
            self.end_headers()
            if self.path.endswith("/responses"):
                events = [
                    {"type": "response.created", "response": {**response, "status": "in_progress", "output": []}},
                    {"type": "response.output_item.added", "output_index": 0, "item": {**output, "status": "in_progress", "content": []}},
                    {"type": "response.output_text.delta", "item_id": "msg_mock", "output_index": 0, "content_index": 0, "delta": text},
                    {"type": "response.output_item.done", "output_index": 0, "item": output},
                    {"type": "response.completed", "response": response},
                ]
                for event in events:
                    self.wfile.write(("event: " + event["type"] + "\ndata: " + json.dumps(event) + "\n\n").encode())
            else:
                chunk = {"id": "chatcmpl-mock", "object": "chat.completion.chunk", "created": 1,
                         "model": "mock-model", "choices": [{"index": 0, "delta": {"role": "assistant", "content": text}, "finish_reason": None}]}
                self.wfile.write(("data: " + json.dumps(chunk) + "\n\n").encode())
                chunk["choices"] = [{"index": 0, "delta": {}, "finish_reason": "stop"}]
                self.wfile.write(("data: " + json.dumps(chunk) + "\n\ndata: [DONE]\n\n").encode())
        else:
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.end_headers()
            self.wfile.write(json.dumps(response if self.path.endswith("/responses") else chat).encode())


def send(port, path, payload=None):
    req = Request(f"http://127.0.0.1:{port}{path}", data=None if payload is None else json.dumps(payload).encode(),
                  headers={"Authorization": "Bearer local-test-client", "Content-Type": "application/json"})
    try:
        with OPENER.open(req, timeout=30) as resp:
            return resp.status, resp.read().decode()
    except HTTPError as err:
        return err.code, err.read().decode()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--cpa", type=Path, required=True)
    parser.add_argument("--plugin", type=Path, required=True)
    parser.add_argument("--artifacts", type=Path, default=Path("dist/simulation"))
    parser.add_argument("--codex", help="Optional Codex executable for a real client metadata smoke test")
    args = parser.parse_args()
    artifacts = args.artifacts.resolve()
    artifacts.mkdir(parents=True, exist_ok=True)
    plugin_dir = artifacts / "plugins"
    plugin_dir.mkdir(parents=True, exist_ok=True)
    shutil.copy2(args.plugin.resolve(), plugin_dir / args.plugin.name)
    auth_dir = artifacts / "auth"
    auth_dir.mkdir(exist_ok=True)
    mock = ThreadingHTTPServer(("127.0.0.1", 0), Mock)
    thread = threading.Thread(target=mock.serve_forever, daemon=True)
    thread.start()
    port = free_port()
    cfg = {"host": "127.0.0.1", "port": port, "auth-dir": str(auth_dir),
           "api-keys": ["local-test-client"], "request-retry": 0,
           "remote-management": {"disable-control-panel": True, "disable-auto-update-panel": True},
           "discovery": {"enabled": False}, "plugins": {"enabled": True,
               "dir": str(artifacts / "plugins"), "configs": {"privacyfilter": {
                   "enabled": True, "max_han_percent": 20, "skip_models": ["exempt-model"]}}},
           "openai-compatibility": [{"name": "mock", "base-url": f"http://127.0.0.1:{mock.server_port}/v1",
               "api-key-entries": [{"api-key": "local-test-upstream", "proxy-url": "direct"}],
               "models": [{"name": "mock-model", "alias": "guard-model"},
                          {"name": "mock-exempt", "alias": "exempt-model"}]}]}
    config_path = artifacts / "config.yaml"
    config_path.write_text(json.dumps(cfg, indent=2))
    env = os.environ.copy()
    for key in list(env):
        if key.lower().endswith("_proxy") or key.startswith(("PGSTORE_", "GITSTORE_", "OBJECTSTORE_")):
            env.pop(key)
    env["NO_PROXY"] = "127.0.0.1,localhost"
    results = []
    with (artifacts / "cpa.log").open("w") as log:
        process = subprocess.Popen([str(args.cpa.resolve()), "--config", str(config_path), "--local-model"],
                                   cwd=artifacts, env=env, stdout=log, stderr=subprocess.STDOUT)
        try:
            for _ in range(200):
                if process.poll() is not None:
                    raise RuntimeError(f"CPA exited with {process.returncode}; inspect {artifacts / 'cpa.log'}")
                try:
                    if send(port, "/v1/models")[0] == 200:
                        break
                except (URLError, TimeoutError, ConnectionError):
                    pass
                time.sleep(0.1)
            else:
                raise RuntimeError("CPA did not start")

            def check(name, items, expected=200, code=None, stream=False, chat=False, model="guard-model"):
                payload = {"model": model, "stream": stream, "messages" if chat else "input": items}
                before = len(Mock.calls)
                status, body = send(port, "/v1/chat/completions" if chat else "/v1/responses", payload)
                calls = len(Mock.calls) - before
                assert status == expected, (name, status, body)
                assert calls == (1 if expected == 200 else 0), (name, "upstream calls", calls)
                if code:
                    error = json.loads(body)["error"]
                    assert error["code"] == code, (name, error)
                    if code == "chinese_ratio_exceeded":
                        assert error["message"] == NOTICE, error
                else:
                    assert "mock English reply" in body, (name, body)
                results.append({"name": name, "status": status, "upstream_calls": calls})
                print(json.dumps(results[-1]), flush=True)

            for stream in (False, True):
                for chat in (False, True):
                    label = f"{'chat' if chat else 'responses'}-{'stream' if stream else 'plain'}"
                    check(label + "-blocked", [message("请用中文回答")], 400, "chinese_ratio_exceeded", stream, chat)
                    check(label + "-english", [message("Please reply in Chinese.")], stream=stream, chat=chat)
                    check(label + "-boundary", [message("中abcd")], stream=stream, chat=chat)
                    check(label + "-above", [message("中abc 123456，🙂")], 400, "chinese_ratio_exceeded", stream, chat)
            check("context-excluded", [message("English"), message("中文环境", "environments.environment_context"),
                                      message("中文技能", "skills.selected_skill_instructions")])
            check("history-does-not-dilute", [message("English " * 100), message("中文")], 400, "chinese_ratio_exceeded")
            check("old-chinese-does-not-block", [message("中文历史"), message("English")])
            check("metadata-required", [message("old English"), {"role": "user", "content": "English"}], 400, "input_attribution_unavailable")
            check("string-input-rejected", "English", 400, "input_attribution_unavailable")
            check("explicit-skip", "中文", model="exempt-model")
            check("quoted-exemption", [message("  > 中文题目\n中文回答")])
            check("quote-not-at-start", [message("中文指令\n> 中文题目")], 400, "chinese_ratio_exceeded")
            check("no-redaction", [message("Contact person@example.com")])
            assert "person@example.com" in json.dumps(Mock.calls[-1]["body"])
            image = message("unused")
            image["content"] = [{"type": "input_text", "text": '<image name=[Image #1] path="/tmp/中文.png">'},
                                {"type": "input_image", "image_url": "data:image/png;base64,AA=="},
                                {"type": "input_text", "text": "</image>"},
                                {"type": "input_text", "text": "中abc"}]
            image["internal_chat_message_metadata_passthrough"]["content_item_kinds"] = ["user.text", "user.image", "user.text", "user.text"]
            check("image-wrapper-does-not-dilute", [image], 400, "chinese_ratio_exceeded")
            check("tool-continuation", [message("English"), {"type": "function_call", "call_id": "c1", "name": "example", "arguments": "{}"},
                                         {"type": "function_call_output", "call_id": "c1", "output": "中文工具结果"}])
            if args.codex:
                workspace = artifacts / "codex-workspace"
                workspace.mkdir(exist_ok=True)
                client_env = env.copy()
                client_env["CPA_LANGUAGE_GUARD_TEST_KEY"] = "local-test-client"
                for suffix, provider_name, prompt, expected_calls, expected_code in [
                    ("english", "OpenAI", "Please reply in Chinese.", 1, None),
                    ("chinese", "OpenAI", "请用中文回答", 0, "chinese_ratio_exceeded"),
                    ("quoted", "OpenAI", "> 中文题目\n中文回答", 1, None),
                    ("missing-metadata", "Custom", "Please reply in Chinese.", 0, "input_attribution_unavailable"),
                ]:
                    label = f"codex-{provider_name}-{suffix}"
                    provider = '{name="' + provider_name + '",base_url="http://127.0.0.1:' + str(port) + '/v1",env_key="CPA_LANGUAGE_GUARD_TEST_KEY",wire_api="responses",requires_openai_auth=false,supports_websockets=false,request_max_retries=0,stream_max_retries=0}'
                    command = [args.codex, "exec", "--ignore-user-config", "--ephemeral", "--skip-git-repo-check",
                               "--sandbox", "read-only", "--cd", str(workspace), "--disable", "hooks",
                               "-c", 'model_provider="cpa_language_guard"', "-c", "model_providers.cpa_language_guard=" + provider,
                               "-c", 'model="guard-model"', "-c", "features.content_item_kinds=true", "--json", prompt]
                    before = len(Mock.calls)
                    run = subprocess.run(command, stdin=subprocess.DEVNULL, capture_output=True, text=True,
                                         env=client_env, timeout=60)
                    # Store client diagnostics, never request headers or authentication.
                    (artifacts / (label + ".log")).write_text(run.stdout + run.stderr)
                    calls = len(Mock.calls) - before
                    assert calls == expected_calls, (label, calls, run.stdout[-1500:], run.stderr[-1500:])
                    if expected_code:
                        assert run.returncode != 0 and expected_code in run.stdout + run.stderr, (label, run.stdout, run.stderr)
                    else:
                        assert run.returncode == 0 and "mock English reply" in run.stdout, (label, run.stdout, run.stderr)
                    results.append({"name": label, "client_exit": run.returncode, "upstream_calls": calls})
                    print(json.dumps(results[-1]), flush=True)
        finally:
            process.terminate()
            try:
                process.wait(timeout=10)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait()
            mock.shutdown()
            mock.server_close()
            thread.join(timeout=5)
            (artifacts / "results.json").write_text(json.dumps(results, ensure_ascii=False, indent=2))
    print(f"PASS: {len(results)} scenarios; artifacts: {artifacts}")


if __name__ == "__main__":
    main()
