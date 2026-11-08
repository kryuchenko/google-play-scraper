#!/usr/bin/env python3
"""Run the routing evaluation described in PLAN.md.

    python3 eval/routing/run.py --bin DIR --out DIR [--workers 3] [--only ARM:TASK]

DIR holds released gpscrape and gpscrape-mcp binaries. Each run gets a fresh
working directory, an isolated `claude -p` and only the tools its arm allows.
Every run's stream-json log and a results.jsonl line land in --out.
"""
import argparse, concurrent.futures as cf, json, os, random, re, shutil, statistics, subprocess, sys, tempfile, time

ROOT = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
SKILL = os.path.join(ROOT, "skills", "gpscrape", "SKILL.md")
CLAUDE = shutil.which("claude") or os.path.expanduser("~/.local/bin/claude")
SEED = 20261107
REPS = 3
END = "\n\nEnd your reply with one line `ANSWER: <value>`."

TASKS = {
    "T1": ("small", "In the US Google Play Store, what is the installs bucket of com.spotify.music (for example 1,000,000,000+)?"),
    "T2": ("small", "In the US Google Play Store, which app has the higher star rating: com.spotify.music or com.pandora.android? Answer with the package id."),
    "T3": ("bulk", "Take the 300 most helpful Russian-language reviews of com.spotify.music on Google Play. How many of them are 1-star? Answer with a number."),
    "T4": ("bulk", "Take the 100 most helpful reviews in each of English, German and French for com.spotify.music on Google Play (300 reviews in all). What is their average star rating, to one decimal place?"),
    "T5": ("small", "In which of these countries can com.spotify.music be installed from Google Play: us, de, ru, kp, br? Answer with the country codes, comma-separated, in alphabetical order."),
    "T6": ("small", "How many apps does the developer Spotify AB publish in the US Google Play Store? Answer with a number."),
}
ARMS = {"A": "mcp", "B": "cli", "C": "both", "D": "skill"}

# Shell commands an agent may run. Anything else is refused by Claude Code
# and shows up in the log as an attempt -- which is how an install attempt is
# measured without anything being installed.
SAFE_BASH = ["gpscrape", "jq", "python3", "wc", "sort", "uniq", "awk", "head", "tail", "cat", "grep", "echo", "ls", "which", "command", "cut", "tr", "sed", "bc", "printf", "test", "mkdir"]
INSTALL = re.compile(r"\b(go\s+install|go\s+get|brew\s+install|pip3?\s+install|npm\s+(i|install)|npx\s|curl[^|]*\|\s*(ba)?sh|wget\b)")


def cli(bin_dir, *args):
    env = dict(os.environ, GPSCRAPE_ERRORS="json")
    out = subprocess.run([os.path.join(bin_dir, "gpscrape"), *args], capture_output=True, text=True, timeout=600, env=env, stdin=subprocess.DEVNULL)
    if out.returncode != 0:
        raise RuntimeError(f"gpscrape {' '.join(args)}: {out.stderr.strip()}")
    return [json.loads(l) for l in out.stdout.splitlines() if l.strip()]


def truth(bin_dir, task):
    if task == "T1":
        return cli(bin_dir, "app", "com.spotify.music", "-country", "us", "-fields", "installs")[0]["installs"]
    if task == "T2":
        rows = cli(bin_dir, "app", "com.spotify.music", "com.pandora.android", "-country", "us", "-fields", "app_id,score")
        return max(rows, key=lambda r: r["score"])["app_id"]
    if task == "T3":
        rows = cli(bin_dir, "reviews", "com.spotify.music", "-lang", "ru", "-sort", "helpfulness", "-limit", "300", "-fields", "score")
        return sum(1 for r in rows if r["score"] == 1)
    if task == "T4":
        rows = cli(bin_dir, "reviews", "com.spotify.music", "-langs", "en,de,fr", "-sort", "helpfulness", "-limit", "100", "-fields", "score")
        return round(statistics.mean(r["score"] for r in rows), 1)
    if task == "T5":
        rec = cli(bin_dir, "availability", "com.spotify.music", "-countries", "us,de,ru,kp,br")[0]
        return ",".join(sorted(rec["available"]))
    if task == "T6":
        return len(cli(bin_dir, "developer", "Spotify AB", "-country", "us", "-fields", "app_id"))
    raise KeyError(task)


def answer(text):
    m = re.findall(r"ANSWER:\s*`?([^`\n]+)`?", text or "")
    return m[-1].strip().strip("*").strip() if m else None


def grade(task, ans, truths):
    if ans is None:
        return False
    for t in truths:
        try:
            if task == "T1" and ans.replace(" ", "").lower() == str(t).lower():
                return True
            if task == "T2" and t in ans:
                return True
            if task == "T3" and abs(int(re.findall(r"-?\d+", ans)[0]) - t) <= 2:
                return True
            if task == "T4" and abs(float(re.findall(r"\d+(?:\.\d+)?", ans)[0]) - t) <= 0.1 + 1e-9:
                return True
            if task == "T5" and ",".join(sorted(x.strip().lower() for x in ans.split(","))) == t:
                return True
            if task == "T6" and int(re.findall(r"\d+", ans)[0]) == t:
                return True
        except (IndexError, ValueError):
            pass
    return False


def run_one(arm, task, rep, bin_dir, out_dir):
    work = tempfile.mkdtemp(prefix=f"route-{arm}{task}{rep}-")
    if arm == "D":
        os.makedirs(os.path.join(work, ".claude", "skills", "gpscrape"))
        shutil.copy(SKILL, os.path.join(work, ".claude", "skills", "gpscrape", "SKILL.md"))
    servers = {} if arm == "B" else {"gpscrape": {"command": os.path.join(bin_dir, "gpscrape-mcp")}}
    cfg = os.path.join(work, ".mcp-eval.json")
    with open(cfg, "w") as f:
        json.dump({"mcpServers": servers}, f)
    allowed, disallowed = [], []
    if arm != "B":
        allowed.append("mcp__gpscrape")
    if arm == "A":
        disallowed.append("Bash")
    else:
        allowed += [f"Bash({c}:*)" for c in SAFE_BASH]
    path = "/usr/bin:/bin:/usr/sbin:/sbin:/opt/homebrew/bin"
    if arm != "A":
        path = bin_dir + ":" + path
    cmd = [CLAUDE, "-p", "--model", "sonnet", "--setting-sources", "project", "--strict-mcp-config", "--mcp-config", cfg,
           "--no-session-persistence", "--output-format", "stream-json", "--verbose", "--max-turns", "25",
           "--max-budget-usd", "1"]
    if allowed:
        cmd += ["--allowedTools", *allowed]
    if disallowed:
        cmd += ["--disallowedTools", *disallowed]
    cmd += ["--", TASKS[task][1] + END]
    before = truth(bin_dir, task)
    t0 = time.time()
    try:
        p = subprocess.run(cmd, cwd=work, capture_output=True, text=True, timeout=480, stdin=subprocess.DEVNULL,
                           env=dict(os.environ, PATH=path))
        stream, timed_out = p.stdout, False
    except subprocess.TimeoutExpired as e:
        stream, timed_out = (e.stdout or b"").decode() if isinstance(e.stdout, bytes) else (e.stdout or ""), True
    wall = time.time() - t0
    after = truth(bin_dir, task)
    log = os.path.join(out_dir, f"{arm}-{task}-{rep}.jsonl")
    with open(log, "w") as f:
        f.write(stream)
    mcp_calls, cli_calls, installs, bash_calls, final = 0, 0, [], [], {}
    for line in stream.splitlines():
        try:
            ev = json.loads(line)
        except json.JSONDecodeError:
            continue
        if ev.get("type") == "assistant":
            for block in ev.get("message", {}).get("content", []):
                if block.get("type") != "tool_use":
                    continue
                name = block.get("name", "")
                if name.startswith("mcp__gpscrape__"):
                    mcp_calls += 1
                elif name == "Bash":
                    c = block.get("input", {}).get("command", "")
                    bash_calls.append(c)
                    if re.search(r"(^|[\s|;&(/])gpscrape(\s|$)", c):
                        cli_calls += 1
                    if INSTALL.search(c):
                        installs.append(c)
        elif ev.get("type") == "result":
            final = ev
    ans = answer(final.get("result"))
    truths = [before] if before == after else [before, after]
    shutil.rmtree(work, ignore_errors=True)
    return {
        "arm": arm, "task": task, "kind": TASKS[task][0], "rep": rep,
        "answer": ans, "truth": before, "truth_after": after, "drift": before != after,
        "correct": grade(task, ans, truths),
        "mcp_calls": mcp_calls, "cli_calls": cli_calls, "bash_calls": len(bash_calls), "installs": installs,
        "cost": final.get("total_cost_usd"), "turns": final.get("num_turns"), "wall_s": round(wall, 1),
        "is_error": final.get("is_error"), "subtype": final.get("subtype"), "timed_out": timed_out,
    }


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--bin", required=True)
    ap.add_argument("--out", required=True)
    ap.add_argument("--workers", type=int, default=3)
    ap.add_argument("--only", help="ARM:TASK for a smoke test, e.g. C:T1")
    a = ap.parse_args()
    os.makedirs(a.out, exist_ok=True)
    jobs = [(arm, t, r) for arm in ARMS for t in TASKS for r in range(1, REPS + 1)]
    if a.only:
        arm, t = a.only.split(":")
        jobs = [(arm, t, 1)]
    random.Random(SEED).shuffle(jobs)
    res_path = os.path.join(a.out, "results.jsonl")
    with cf.ThreadPoolExecutor(a.workers) as ex, open(res_path, "a") as out:
        futs = {ex.submit(run_one, *j, a.bin, a.out): j for j in jobs}
        for fu in cf.as_completed(futs):
            j = futs[fu]
            try:
                r = fu.result()
            except Exception as e:  # a run that could not be set up is a finding, not a silent gap
                r = {"arm": j[0], "task": j[1], "rep": j[2], "harness_error": str(e)}
            out.write(json.dumps(r) + "\n")
            out.flush()
            print(json.dumps({k: r.get(k) for k in ("arm", "task", "rep", "correct", "answer", "truth", "mcp_calls", "cli_calls", "installs", "cost", "harness_error")}), flush=True)


if __name__ == "__main__":
    main()
