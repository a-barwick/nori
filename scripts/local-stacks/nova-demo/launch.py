#!/usr/bin/env python3
"""Launch the local, synthetic Nova demo and keep it running until interrupted."""

from contextlib import ExitStack, closing
import http.client
from http.server import ThreadingHTTPServer
import json
import os
from pathlib import Path
import shutil
import signal
import socket
import subprocess
import sys
from tempfile import TemporaryDirectory
from threading import Thread
import time
from urllib.parse import parse_qs, urlencode, urlsplit
from uuid import uuid4

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
    with closing(http.client.HTTPConnection("127.0.0.1", port, timeout=5)) as client:
        client.request(method, path, body, headers)
        response = client.getresponse()
        payload = response.read()
    if response.status != status:
        raise RuntimeError(f"{method} {path.split('?')[0]} returned {response.status}: {payload[:500]!r}")
    if response.getheader("Content-Type", "").startswith("application/json"):
        return json.loads(payload)


def connect_google(port, user):
    path = f"/users/{user}/v1/connections"
    created = request(port, "POST", path, {
        "request_id": str(uuid4()), "expected_revision": 0,
        "value": {"integration_id": "google", "title": "Demo Google", "selected_resource_ids": []},
    }, status=201)["resource"]
    connection = f"{path}/{created['id']}"
    consent = request(port, "POST", f"{connection}/actions", {
        "request_id": str(uuid4()), "expected_revision": created["revision"],
        "value": {
            "action": "connect", "return_to": "/apps",
            "capabilities": ["calendar.read", "mail.read", "mail.draft", "mail.send"],
        },
    }, status=202)["auth"]
    # Use the recorded OAuth exchange locally without opening the authorize URL.
    state = parse_qs(urlsplit(consent["authorize_url"]).query)["state"][0]
    query = urlencode({"state": state, "code": "recorded-mail-code"})
    request(port, "GET", f"{path}/oauth/callback?{query}", status=303)
    if request(port, "GET", connection)["status"] != "connected":
        raise RuntimeError("The synthetic Google connection did not become connected")


def launch(work):
    project = f"nori-nova-{uuid4().hex[:12]}"
    user = str(uuid4())
    build = "nova-demo"
    compose = (
        "docker", "compose", "--env-file", os.devnull, "--project-name", project,
        "-f", CORE / "infrastructure/local/compose.yaml", "-f", HERE / "compose.override.yaml",
    )

    print("Building Nori API, worker and user provisioner...", flush=True)
    run("go", "build", "-o", str(work) + os.sep,
        "./cmd/nori-local", "./cmd/nori-worker", "./cmd/nori-user", cwd=CORE, timeout=600)
    recordings = work / "recordings"
    shutil.copytree(ROOT / "contracts/assistant/recordings", recordings)
    shutil.copyfile(HERE / "draft-readback.json", recordings / "draft-readback.json")

    with ExitStack() as stack:
        print(f"Starting Postgres and Temporal in {project}...", flush=True)
        stack.callback(run, *compose, "down", "--volumes", "--timeout", "5", cwd=work)
        run(*compose, "up", "-d", "--wait", "--wait-timeout", "75", cwd=work, timeout=240)
        postgres = run(*compose, "port", "postgres", "5432", cwd=work)
        temporal = run(*compose, "port", "temporal", "7233", cwd=work)

        stub = stack.enter_context(ThreadingHTTPServer(("127.0.0.1", 0), ModelStub))
        Thread(target=stub.serve_forever, daemon=True).start()
        stack.callback(stub.shutdown)
        model_url = f"http://127.0.0.1:{stub.server_port}"
        env = {
            "NORI_ENV_FILE": os.devnull,
            "NORI_CLOUD": "local",
            "NORI_STORE_BACKEND": "postgres",
            "NORI_POSTGRES_URL": f"postgres://nori@{postgres}/nori?sslmode=disable",
            "NORI_LOCAL_ARTIFACT_DIR": str(work / "artifacts"),
            "NORI_CREDENTIAL_STORE": "local",
            "NORI_PROVIDER_REPLAY": str(recordings),
            "GOOGLE_CALENDAR_CLIENT_ID": "recorded-google-client",
            "GOOGLE_CALENDAR_CLIENT_SECRET": "recorded-google-secret",
            "NORI_USER_SENDS": "ask",
            "NORI_MAIL_RECIPIENTS": "self",
            "NORI_BACKGROUND_TASKS_ENABLED": "true",
            "NORI_COMPUTER_ENABLED": "false",
            "NORI_JEV_ENABLED": "false",
            "OPENAI_API_KEY": "demo-placeholder",
            "MODEL_NAME": "gpt-6.1-terra",
            "NORI_MODEL_TEST_URL": model_url,
            "TEMPORAL_LOCAL": "true",
            "TEMPORAL_ADDRESS": temporal,
            "TEMPORAL_NAMESPACE": "default",
            "TEMPORAL_TASK_QUEUE": project,
            "TEMPORAL_DEPLOYMENT_NAME": project,
            "TEMPORAL_BUILD_ID": build,
            # The stub never forwards requests, including proxy CONNECT.
            "HTTP_PROXY": model_url,
            "HTTPS_PROXY": model_url,
            "NO_PROXY": "127.0.0.1,localhost",
        }

        print("Provisioning the demo user and Google connection...", flush=True)
        run(work / "nori-user", "-id", user, "-email", ACCOUNT, "-name", "Nova Demo",
            cwd=work, env=env)
        # Docker assigns dependency ports; the host API needs an explicit port.
        with socket.socket() as listener:
            listener.bind(("127.0.0.1", 0))
            port = listener.getsockname()[1]
        base = f"http://127.0.0.1:{port}"
        scoped = f"{base}/users/{user}"
        env["NORI_OAUTH_REDIRECT_URL"] = f"{scoped}/v1/connections/oauth/callback"
        api = start(stack, work, "nori-local", {**env, "PORT": str(port)})
        wait_ready("API", api, lambda: request(port, "GET", "/healthz"))
        connect_google(port, user)

        print("Starting the worker and activating its build...", flush=True)
        worker = start(stack, work, "nori-worker", env)
        wait_ready("worker build", worker, lambda: run(
            *compose, "exec", "-T", "temporal", "temporal", "worker", "deployment",
            "set-current-version", "--address", "127.0.0.1:7233", "-n", "default",
            "--deployment-name", project, "--build-id", build, "--yes", "--command-timeout", "2s",
            cwd=work, timeout=5,
        ))

        print(
            f"\nNova demo ready\n"
            f"API: {base}\n"
            f"User ID: {user}\n"
            f"User-scoped API: {scoped}/v1\n"
            f"Connected mailbox: {ACCOUNT}\n"
            f"Demo prompt: {PROMPT}\n"
            f"Logs: {work}\n"
            "Press Ctrl-C to stop the demo and remove its data.",
            flush=True,
        )
        while True:
            require_running("API", api)
            require_running("worker", worker)
            time.sleep(1)


def interrupt(*_):
    raise KeyboardInterrupt


def main():
    signal.signal(signal.SIGTERM, interrupt)
    with TemporaryDirectory(prefix="nori-nova-") as tmp:
        work = Path(tmp)
        try:
            launch(work)
        except KeyboardInterrupt:
            print("\nNova demo stopped.")
        except (OSError, RuntimeError, subprocess.TimeoutExpired) as error:
            print(f"Nova demo failed: {error}", file=sys.stderr)
            for log in sorted(work.glob("*.log")):
                print(f"{log.name}:\n" + "\n".join(log.read_text().splitlines()[-12:]), file=sys.stderr)
            return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
