#!/usr/bin/env python3
"""Launch the local, synthetic MCP-only headless demo and keep it running until interrupted."""

from contextlib import ExitStack, closing
import http.client
from http.server import ThreadingHTTPServer
import json
import os
from pathlib import Path
import signal
import socket
import subprocess
import sys
from tempfile import TemporaryDirectory
from threading import Thread
import time
from uuid import uuid4

from mcp_stub import LIST_TOOL_NAME, MCP_SERVER_NAME, MCPStub
from model_stub import ACCOUNT, PROMPT, ModelStub

HERE = Path(__file__).resolve().parent
ROOT = HERE.parents[2]
CORE = ROOT / "core"


def run(*args, cwd, env=None, timeout=30):
    result = subprocess.run(
        args, cwd=cwd, env=env, timeout=timeout, text=True, capture_output=True,
    )
    if result.returncode:
        output = result.stderr.strip() or result.stdout.strip()
        raise RuntimeError(f"{args[0]} exited {result.returncode}: {output}")
    return result.stdout.strip()


def stop(process):
    if process.poll() is None:
        process.terminate()
        try:
            process.wait(timeout=10)
        except subprocess.TimeoutExpired:
            process.kill()
            process.wait()


def start(stack, work, name, env):
    with (work / f"{name}.log").open("w") as log:
        process = subprocess.Popen(
            [work / name], cwd=work, env=env, start_new_session=True,
            stdout=log, stderr=subprocess.STDOUT,
        )
    stack.callback(stop, process)
    return process


def require_running(name, process):
    code = process.poll()
    if code is not None:
        raise RuntimeError(f"{name} exited with status {code}")


def wait_ready(name, process, probe):
    deadline = time.monotonic() + 30
    while True:
        require_running(name, process)
        try:
            return probe()
        except (OSError, RuntimeError, subprocess.TimeoutExpired) as error:
            if time.monotonic() >= deadline:
                raise RuntimeError(f"{name} did not become ready: {error}") from error
        time.sleep(0.25)


def request(port, method, path, data=None, status=200):
    headers = {"Content-Type": "application/json"} if data is not None else {}
    body = json.dumps(data) if data is not None else None
    with closing(http.client.HTTPConnection("127.0.0.1", port, timeout=10)) as client:
        client.request(method, path, body, headers)
        response = client.getresponse()
        payload = response.read()
    if response.status != status:
        raise RuntimeError(f"{method} {path.split('?')[0]} returned {response.status}: {payload[:500]!r}")
    if response.getheader("Content-Type", "").startswith("application/json"):
        return json.loads(payload)


def connect_mcp(port, user, mcp_url):
    """Register the loopback MCP server, run discovery, and allow its read tool."""
    base = f"/users/{user}/v1/mcp"
    created = request(port, "POST", base, {
        "request_id": str(uuid4()),
        "expected_revision": 0,
        "value": {
            "name": MCP_SERVER_NAME,
            "transport": "http",
            "endpoint": mcp_url,
            "auth": "none",
            "enabled": True,
            "exposure": {"default": "direct"},
        },
    }, status=201)["resource"]

    connected = request(port, "POST", f"{base}/{created['id']}/actions", {
        "request_id": str(uuid4()),
        "expected_revision": created["revision"],
        "value": {"action": "connect"},
    }, status=200)["resource"]
    observed = connected.get("observed", {})
    if observed.get("status") != "connected":
        raise RuntimeError(f"The demo MCP server did not connect: {observed!r}")

    tools = request(port, "GET", f"{base}/{created['id']}/tools")["items"]
    list_tool = next((t for t in tools if t["name"] == LIST_TOOL_NAME), None)
    if list_tool is None:
        raise RuntimeError(f"Expected {LIST_TOOL_NAME} in discovered MCP tools: {tools!r}")

    request(port, "POST", f"/users/{user}/v1/permissions", {
        "request_id": str(uuid4()),
        "expected_revision": 0,
        "value": {
            "effect": "allow",
            "tool_ids": [list_tool["definition"]["id"]],
            "connection_ids": [],
            "resource_ids": [],
            "action_classes": ["unknown"],
            "title": "Allow Demo Reports read tool",
        },
    }, status=201)
    return connected


def launch(work):
    project = f"nori-demo-{uuid4().hex[:12]}"
    user = str(uuid4())
    compose = (
        "docker", "compose", "--env-file", os.devnull, "--project-name", project,
        "-f", CORE / "infrastructure/local/compose.yaml", "-f", HERE / "compose.override.yaml",
    )

    print("Building Nori and user provisioner...", flush=True)
    run("go", "build", "-o", str(work) + os.sep,
        "./cmd/nori-local", "./cmd/nori-user", cwd=CORE, timeout=600)

    with ExitStack() as stack:
        print(f"Starting Postgres in {project}...", flush=True)
        stack.callback(run, *compose, "down", "--volumes", "--timeout", "5", cwd=work)
        run(*compose, "up", "-d", "--wait", "--wait-timeout", "75", cwd=work, timeout=240)
        postgres = run(*compose, "port", "postgres", "5432", cwd=work)

        model_server = stack.enter_context(ThreadingHTTPServer(("127.0.0.1", 0), ModelStub))
        Thread(target=model_server.serve_forever, daemon=True).start()
        stack.callback(model_server.shutdown)
        model_url = f"http://127.0.0.1:{model_server.server_port}"

        mcp_server = stack.enter_context(ThreadingHTTPServer(("127.0.0.1", 0), MCPStub))
        Thread(target=mcp_server.serve_forever, daemon=True).start()
        stack.callback(mcp_server.shutdown)
        mcp_url = f"http://127.0.0.1:{mcp_server.server_port}/mcp"

        env = {
            "NORI_ENV_FILE": os.devnull,
            "NORI_CLOUD": "local",
            "NORI_STORE_BACKEND": "postgres",
            "NORI_POSTGRES_URL": f"postgres://nori@{postgres}/nori?sslmode=disable",
            "NORI_LOCAL_ARTIFACT_DIR": str(work / "artifacts"),
            "NORI_BACKGROUND_TASKS_ENABLED": "true",
            "NORI_JEV_ENABLED": "false",
            "OPENAI_API_KEY": "demo-placeholder",
            "MODEL_NAME": "gpt-6.1-terra",
            "NORI_MODEL_TEST_URL": model_url,
        }

        print("Provisioning the demo user and registering the local MCP server...", flush=True)
        run(work / "nori-user", "-id", user, "-email", ACCOUNT, "-name", "Nori Demo",
            cwd=work, env=env)
        with socket.socket() as listener:
            listener.bind(("127.0.0.1", 0))
            port = listener.getsockname()[1]
        base = f"http://127.0.0.1:{port}"
        scoped = f"{base}/users/{user}"
        api = start(stack, work, "nori-local", {**env, "PORT": str(port)})
        wait_ready("API", api, lambda: request(port, "GET", "/healthz"))
        server = connect_mcp(port, user, mcp_url)

        print(
            f"\nHeadless demo ready\n"
            f"API: {base}\n"
            f"User ID: {user}\n"
            f"User-scoped API: {scoped}/v1\n"
            f"MCP server: {server['name']} ({server['id']}) at {mcp_url}\n"
            f"Demo prompt: {PROMPT}\n"
            f"Logs: {work}\n"
            "Press Ctrl-C to stop the demo and remove its data.",
            flush=True,
        )
        while True:
            require_running("API", api)
            time.sleep(1)


def interrupt(*_):
    raise KeyboardInterrupt


def main():
    signal.signal(signal.SIGTERM, interrupt)
    with TemporaryDirectory(prefix="nori-demo-") as tmp:
        work = Path(tmp)
        try:
            launch(work)
        except KeyboardInterrupt:
            print("\nHeadless demo stopped.")
        except (OSError, RuntimeError, subprocess.TimeoutExpired) as error:
            print(f"Headless demo failed: {error}", file=sys.stderr)
            for log in sorted(work.glob("*.log")):
                print(f"{log.name}:\n" + "\n".join(log.read_text().splitlines()[-12:]), file=sys.stderr)
            return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
