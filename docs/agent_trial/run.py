#!/usr/bin/env python3
"""Measures whether docs/agent_map.py helps agents. Each run gets a clean shallow clone with one feature set.

Usage: python3 docs/agent_trial/run.py OUTPUT_DIR [--reps 3] [--workers 5] [--only ID,ID] [--configs base,map]
"""
import argparse
import json
import re
import shutil
import statistics
import subprocess
from concurrent.futures import ThreadPoolExecutor
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
MODEL, EFFORT = "claude-sonnet-5-5", "medium"
FEATURE_SETS = {"base": set(), "map": {"find"}, "lookup": {"find", "code"}, "all": {"find", "code", "stale"}}
ROUTES = {
    "find": 'To look something up, run `python3 docs/agent_map.py find "words"` first. It prints matching doc sections as plain text with their code paths. `docs/agent-map.json` lists every page and section. Read the code it names when you need detail, and avoid reading the whole site.',
    "code": "Before changing a file, or to find its docs, run `python3 docs/agent_map.py code PATH`. It lists the pages that describe that file.",
    "stale": "If its output says STALE, the code changed after the page was reviewed. Check the claim against the code it names, because the code decides what's true.",
}
SEEDS = [
    ("core/cmd/computer-agent/main.go", "15*time.Minute", "10*time.Minute"),
    ("core/internal/task/workflow.go", "turns >= 32", "turns >= 64"),
    ("core/internal/agent/steps.go", "const BudgetUSD = 1.0", "const BudgetUSD = 2.0"),
]
# Each feature is compared with the baseline, and the full set runs on every question.
CONFIGS_FOR = {"map": ["base", "map", "all"], "lookup": ["base", "lookup", "all"], "stale": ["base", "all"]}


def run(*args, cwd):
    subprocess.run(args, cwd=cwd, check=True, capture_output=True, text=True)


def commit(clone, message):
    run("git", "add", "-A", cwd=clone)
    run("git", "-c", "user.name=trial", "-c", "user.email=trial@example.invalid", "commit", "-qm", message, cwd=clone)


def prepare(out, config, seeded):
    clone = out / "clones" / (config + ("-seeded" if seeded else ""))
    shutil.rmtree(clone, ignore_errors=True)
    clone.parent.mkdir(parents=True, exist_ok=True)
    run("git", "clone", "-q", "--depth", "1", f"file://{ROOT}", str(clone), cwd=out)
    run("git", "remote", "remove", "origin", cwd=clone)
    shutil.rmtree(clone / "docs/agent_trial")
    features = FEATURE_SETS[config]
    script, agents = clone / "docs/agent_map.py", clone / "AGENTS.md"
    if features:
        text = script.read_text()
        script.write_text(re.sub(r"FEATURES = \{.*?\}", "FEATURES = {" + ", ".join(f'"{f}"' for f in sorted(features)) + "}", text, count=1))
        route = " ".join(ROUTES[f] for f in ("find", "code", "stale") if f in features)
    else:
        for name in ("docs/agent_map.py", "docs/agent-map.json", "docs/reviewed.json"):
            (clone / name).unlink()
    text = re.sub(r"\n## Looking things up\n.*?(?=\n## )", "", agents.read_text(), flags=re.S)
    if features:
        text = text.replace("## Documentation\n", f"## Looking things up\n\n{route}\n\n## Documentation\n", 1)
    agents.write_text(text)
    commit(clone, "Trial setup")
    if features:
        subprocess.run(["python3", "docs/agent_map.py", "mark", "--all"], cwd=clone, check=True)
        commit(clone, "Record reviews")
    if seeded:
        for name, old, new in SEEDS:
            path = clone / name
            assert old in path.read_text(), name
            path.write_text(path.read_text().replace(old, new, 1))
        commit(clone, "Update settings")
    return clone


def ask(job):
    out, clone, config, question, rep = job
    prompt = question["q"] + "\n\nAnswer from this repository in under 80 words. Don't change any files."
    cmd = ["claude", "-p", prompt, "--model", MODEL, "--effort", EFFORT, "--output-format", "stream-json", "--verbose",
           "--setting-sources", "project", "--strict-mcp-config", "--disable-slash-commands", "--no-session-persistence",
           "--tools", "Bash,Read,Grep,Glob", "--permission-mode", "bypassPermissions"]
    row = {"config": config, "question": question["id"], "feature": question["feature"], "rep": rep}
    try:
        done = subprocess.run(cmd, cwd=clone, capture_output=True, text=True, timeout=420)
    except subprocess.TimeoutExpired:
        row.update(error="timeout", correct=False)
        return row
    tools, used_map, result = 0, False, {}
    for line in done.stdout.splitlines():
        event = json.loads(line) if line.startswith("{") else {}
        if event.get("type") == "assistant":
            for block in event["message"].get("content", []):
                if block.get("type") == "tool_use":
                    tools += 1
                    used_map = used_map or "agent_map" in json.dumps(block.get("input")) or "agent-map" in json.dumps(block.get("input"))
        if event.get("type") == "result":
            result = event
    usage = result.get("usage", {})
    answer = result.get("result", "")
    row.update(answer=answer, correct=bool(answer) and all(re.search(p, answer, re.I) for p in question["all"]), tools=tools, used_map=used_map,
               tokens=sum(usage.get(k, 0) for k in ("input_tokens", "cache_read_input_tokens", "cache_creation_input_tokens", "output_tokens")),
               output=usage.get("output_tokens", 0), cost=result.get("total_cost_usd"), seconds=(result.get("duration_ms") or 0) / 1000, error=None if result else done.stderr[-300:])
    with (out / "results.jsonl").open("a") as file:
        file.write(json.dumps(row) + "\n")
    print(config, question["id"], rep, "ok" if row["correct"] else "WRONG", row["tokens"], flush=True)
    return row


def summarize(out):
    rows = [json.loads(line) for line in (out / "results.jsonl").read_text().splitlines()]
    lines = ["| Feature | Config | Runs | Correct | Median tokens | Median tools | Median seconds | Median cost | Used map |", "|---|---|---:|---:|---:|---:|---:|---:|---:|"]
    for feature in CONFIGS_FOR:
        for config in FEATURE_SETS:
            group = [r for r in rows if r["feature"] == feature and r["config"] == config]
            if group:
                med = lambda key: statistics.median(r.get(key) or 0 for r in group)
                lines.append(f"| {feature} | {config} | {len(group)} | {sum(r['correct'] for r in group)}/{len(group)} | {med('tokens'):,.0f} | {med('tools'):.0f} | {med('seconds'):.0f} | ${med('cost'):.3f} | {sum(bool(r.get('used_map')) for r in group)}/{len(group)} |")
    text = "\n".join(lines) + "\n"
    (out / "summary.md").write_text(text)
    print(text)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("out", type=Path)
    parser.add_argument("--reps", type=int, default=3)
    parser.add_argument("--workers", type=int, default=5)
    parser.add_argument("--only")
    parser.add_argument("--configs")
    args = parser.parse_args()
    args.out.mkdir(parents=True, exist_ok=True)
    questions = json.loads((ROOT / "docs/agent_trial/questions.json").read_text())
    jobs, clones = [], {}
    for question in questions:
        if args.only and question["id"] not in args.only.split(","):
            continue
        for config in CONFIGS_FOR[question["feature"]]:
            if args.configs and config not in args.configs.split(","):
                continue
            key = (config, bool(question.get("seeded")))
            clones.setdefault(key, prepare(args.out, *key))
            jobs += [(args.out, clones[key], config, question, rep) for rep in range(args.reps)]
    with ThreadPoolExecutor(args.workers) as pool:
        list(pool.map(ask, jobs))
    summarize(args.out)


if __name__ == "__main__":
    main()
