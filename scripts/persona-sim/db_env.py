#!/usr/bin/env python3
"""Process, HTTP, ephemeral Postgres, and runtime environment helpers for persona-sim."""

from __future__ import annotations

import http.client
import json
import os
import socket
import subprocess
import threading
from contextlib import ExitStack, closing
from datetime import datetime, timezone
from pathlib import Path
from typing import Any
from urllib.parse import urlparse
from uuid import uuid4

from llm_client import strip_raw_provider_keys

ROOT = Path(__file__).resolve().parents[2]
CORE = ROOT / "core"
PERSONAS_DIR = ROOT / "sim" / "personas"
RUNS_DIR = ROOT / ".sim-runs"

_db_init_lock = threading.Lock()


def pick_free_port() -> int:
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as s:
        s.bind(("127.0.0.1", 0))
        return int(s.getsockname()[1])


def run_cmd(*args: str, cwd: Path, env: dict[str, str] | None = None, timeout: int = 300) -> str:
    res = subprocess.run(args, cwd=cwd, env=env, timeout=timeout, text=True, capture_output=True)
    if res.returncode != 0:
        out = (res.stderr or res.stdout or "").strip()
        raise RuntimeError(f"{' '.join(args)} exited {res.returncode}: {out[:800]}")
    return res.stdout.strip()


def http_json(
    base_url: str,
    method: str,
    path: str,
    data: Any = None,
    raw_bytes: bytes | None = None,
    content_type: str | None = None,
    timeout: int = 120,
) -> tuple[int, Any]:
    parsed = urlparse(base_url)
    host = parsed.hostname or "127.0.0.1"
    port = parsed.port or (443 if parsed.scheme == "https" else 80)
    headers: dict[str, str] = {}
    body: bytes | None = None
    if raw_bytes is not None:
        body = raw_bytes
        headers["Content-Type"] = content_type or "application/octet-stream"
    elif data is not None:
        body = json.dumps(data, ensure_ascii=False).encode("utf-8")
        headers["Content-Type"] = "application/json"

    with closing(http.client.HTTPConnection(host, port, timeout=timeout)) as conn:
        conn.request(method, path, body=body, headers=headers)
        resp = conn.getresponse()
        payload = resp.read()
        status = resp.status
        ctype = resp.getheader("Content-Type", "")
    if ctype.startswith("application/json") and payload:
        try:
            return status, json.loads(payload)
        except json.JSONDecodeError:
            return status, payload.decode("utf-8", errors="replace")
    return status, payload.decode("utf-8", errors="replace")


def _supports_user_id_flag(nori_sim_bin: Path) -> bool:
    """Detect whether the compiled nori-sim binary accepts --user-id."""
    try:
        res = subprocess.run(
            [str(nori_sim_bin), "-h"],
            cwd=CORE,
            timeout=15,
            text=True,
            capture_output=True,
        )
        return "--user-id" in ((res.stdout or "") + (res.stderr or ""))
    except Exception:
        return False


def build_binaries(bin_dir: Path) -> tuple[Path, Path]:
    print("Building cmd/nori-sim and cmd/nori-user...", flush=True)
    run_cmd("go", "build", "-o", str(bin_dir) + os.sep, "./cmd/nori-sim", "./cmd/nori-user", cwd=CORE, timeout=600)
    return bin_dir / "nori-sim", bin_dir / "nori-user"


def ensure_ephemeral_postgres(
    stack: ExitStack,
    dotenv: dict[str, str],
    keep_db: bool,
    nori_sim_bin: Path | None = None,
) -> tuple[str, str | None]:
    """Return (postgres_url_for_ephemeral_db, db_name_to_drop_or_none).

    Prefer creating a fresh ephemeral database on an already-running local loopback Postgres
    (e.g. `make deps` on 127.0.0.1:15432); otherwise start an isolated Docker Compose Postgres.
    Never touches production or the default `nori` database.
    """
    raw_url = os.environ.get("NORI_POSTGRES_URL") or dotenv.get("NORI_POSTGRES_URL") or "postgres://nori@127.0.0.1:15432/nori?sslmode=disable"
    parsed = urlparse(raw_url)
    host = parsed.hostname or "127.0.0.1"
    port = parsed.port or 15432
    user = parsed.username or "nori"
    password = parsed.password or ""
    db_name = f"nori_sim_{uuid4().hex[:10]}"

    local_ok = False
    if host in ("127.0.0.1", "localhost"):
        try:
            with socket.create_connection((host, port), timeout=1.5):
                local_ok = True
        except OSError:
            local_ok = False

    if local_ok:
        auth = f"{user}:{password}@" if password else f"{user}@"
        target_url = f"postgres://{auth}{host}:{port}/{db_name}?sslmode=disable"
        admin_url = f"postgres://{auth}{host}:{port}/postgres?sslmode=disable"
        created = False

        if nori_sim_bin is not None:
            try:
                with _db_init_lock:
                    run_cmd(str(nori_sim_bin), "--create-db", target_url, cwd=ROOT, timeout=30)
                created = True
                if not keep_db:
                    stack.callback(
                        lambda u=target_url, b=str(nori_sim_bin): subprocess.run(
                            [b, "--drop-db", u], cwd=ROOT, capture_output=True, text=True, timeout=30
                        )
                    )
            except Exception:
                created = False

        if not created:
            try:
                with _db_init_lock:
                    run_cmd("psql", admin_url, "-v", "ON_ERROR_STOP=1", "-c", f'CREATE DATABASE "{db_name}";', cwd=ROOT, timeout=30)
                created = True
                if not keep_db:
                    stack.callback(
                        lambda: subprocess.run(
                            ["psql", admin_url, "-c", f'DROP DATABASE IF EXISTS "{db_name}" WITH (FORCE);'],
                            cwd=ROOT, capture_output=True, text=True, timeout=30,
                        )
                    )
            except Exception:
                created = False

        if created:
            return target_url, db_name

    project = f"nori-sim-{uuid4().hex[:10]}"
    compose = (
        "docker", "compose", "--env-file", os.devnull, "--project-name", project,
        "-f", str(CORE / "infrastructure/local/compose.yaml"),
        "-f", str(ROOT / "scripts/local-stacks/headless-demo/compose.override.yaml"),
    )
    print(f"Starting isolated ephemeral Postgres container ({project})...", flush=True)
    if not keep_db:
        stack.callback(
            lambda: subprocess.run([*compose, "down", "--volumes", "--timeout", "5"], cwd=ROOT, capture_output=True, text=True)
        )
    run_cmd(*compose, "up", "-d", "--wait", "--wait-timeout", "75", cwd=ROOT, timeout=240)
    host_port = run_cmd(*compose, "port", "postgres", "5432", cwd=ROOT, timeout=30).splitlines()[-1].strip()
    run_cmd(
        *compose, "exec", "-T", "postgres", "psql", "-U", "nori", "-d", "postgres",
        "-v", "ON_ERROR_STOP=1", "-c", f'CREATE DATABASE "{db_name}";',
        cwd=ROOT, timeout=30,
    )
    return f"postgres://nori@{host_port}/{db_name}?sslmode=disable", db_name


def _parse_iso_utc(raw: str) -> datetime | None:
    s = str(raw or "").strip()
    if not s:
        return None
    if s.endswith("Z"):
        s = s[:-1] + "+00:00"
    try:
        dt = datetime.fromisoformat(s)
        if dt.tzinfo is None:
            dt = dt.replace(tzinfo=timezone.utc)
        return dt.astimezone(timezone.utc)
    except ValueError:
        return None


def _clip_utf8(text: str, max_bytes: int) -> str:
    raw = text.encode("utf-8")
    if len(raw) <= max_bytes:
        return text
    clipped = raw[:max_bytes]
    while clipped:
        try:
            return clipped.decode("utf-8")
        except UnicodeDecodeError:
            clipped = clipped[:-1]
    return ""


def persona_runtime_env(
    dotenv: dict[str, str], postgres_url: str, artifacts_dir: Path, cred_key_file: Path,
    email: str, port: int, stub_model_url: str | None,
) -> dict[str, str]:
    env = strip_raw_provider_keys(dict(os.environ))
    for k, v in strip_raw_provider_keys(dotenv).items():
        env.setdefault(k, v)
    for forbidden in (
        "NORI_MODEL_GATEWAY_URL",
        "NORI_MODEL_GATEWAY_API_KEY",
        "NORI_PLATFORM_AI_GATEWAY_KEY",
        "NORI_MODEL_PROVIDER",
        "NORI_OUTER_PROVIDER",
        "NORI_INNER_PROVIDER",
    ):
        env.pop(forbidden, None)
    env.update({
        "NORI_ENV_FILE": os.devnull,
        "NORI_TASK_ENV_FILE": os.devnull,
        "NORI_CLOUD": "local",
        "NORI_STORE_BACKEND": "postgres",
        "NORI_POSTGRES_URL": postgres_url,
        "NORI_LOCAL_ARTIFACT_DIR": str(artifacts_dir),
        "NORI_CREDENTIAL_STORE": "database",
        "NORI_CREDENTIAL_KEY_FILE": str(cred_key_file),
        "NORI_LOCAL_EMAIL": email,
        "PORT": str(port),
        "NORI_BACKGROUND_TASKS_ENABLED": "true",
        "NORI_JEV_ENABLED": "false" if stub_model_url else env.get("NORI_JEV_ENABLED", "false"),
    })
    if stub_model_url:
        env.update({
            "NORI_MODEL_TEST_URL": stub_model_url,
            "NORI_ENV_PROFILE": "test",
            "NORI_AGENT_MODEL": "gpt-6.1-sol",
            "MODEL_NAME": "gpt-6.1-terra",
            "NORI_MODEL_PROFILES": "",
            "NORI_MODEL_PROFILES_FILE": "",
            "NORI_LOCAL_MODEL_URL": "",
            "NORI_LOCAL_MODEL_TAG": "",
            "NORI_LLM_MAX_USD": "",
        })
    return env
