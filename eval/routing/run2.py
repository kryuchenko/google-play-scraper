#!/usr/bin/env python3
"""Run the second routing evaluation described in PLAN2.md.

    python3 eval/routing/run2.py --bin DIR --out DIR [--workers 2] [--only ARM:TASK] [--probe]

Differences from run.py, each fixing a flaw the first run's reviewers found:
every arm has a shell, confined by Claude Code's OS sandbox (writes to the
working and temp directories, network to play.google.com only) instead of a
command allow-list an interpreter could escape; CLI calls count only when a
real subcommand ran and returned without error; Skill reads are detected;
`pip download` and `python -m pip` count as install attempts.
"""
import argparse, concurrent.futures as cf, json, os, random, re, shutil, statistics, subprocess, tempfile, time

ROOT = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
SKILL = os.path.join(ROOT, "skills", "gpscrape", "SKILL.md")
CLAUDE = shutil.which("claude") or os.path.expanduser("~/.local/bin/claude")
SEED = 20261108
REPS = 3
END = "\n\nEnd your reply with one line `ANSWER: <value>`."

TASKS = {
    "T7": "Take the 3,000 most helpful Russian-language reviews of com.spotify.music on Google Play. How many of them are 1-star? Answer with a number.",
    "T8": "Take the 100 most helpful reviews of com.whatsapp in every review language Google Play serves. What is their average star rating, to two decimal places?",
    "T9": "Take the 2,000 most helpful English-language reviews of com.instagram.android on Google Play. What percentage of them contain the word \"ad\" or \"ads\" (case-insensitive, as a whole word) in their text? Answer in percent to one decimal place.",
}
ARMS = ("A", "C", "D")

SANDBOX = {
    "sandbox": {
        "enabled": True,
        "autoAllowBashIfSandboxed": True,
        "allowUnsandboxedCommands": False,
        "failIfUnavailable": True,
        # Go on macOS verifies TLS through the system trust service, which
        # Seatbelt blocks; without this every gpscrape request failed with
        # x509 OSStatus -26276 (run 2a). Hosts are still limited below.
        "enableWeakerNetworkIsolation": True,
        "network": {"allowedDomains": ["play.google.com"], "strictAllowlist": True},
    }
}
INSTALL = re.compile(r"\b(go\s+(install|get)|brew\s+install|pip3?\s+(install|download)|python3?\s+-m\s+pip|npm\s+(i|install)\b|npx\s|(curl|wget)[^|]*\|\s*(ba)?sh)")
CLI_DATA = re.compile(r"(^|[\s|;&(/])gpscrape\s+(reviews|app|search|list|similar|developer|permissions|datasafety|availability|catalog|suggest|categories|pcapp)\b")


def cli(bin_dir, *args):
    env = dict(os.environ, GPSCRAPE_ERRORS="json")
    out = subprocess.run([os.path.join(bin_dir, "gpscrape"), *args], capture_output=True, text=True, timeout=1800, env=env, stdin=subprocess.DEVNULL)
    if out.returncode != 0:
        raise RuntimeError(f"gpscrape {' '.join(args)}: {out.stderr.strip()}")
    return [json.loads(l) for l in out.stdout.splitlines() if l.strip()]


def truth(bin_dir, task):
    if task == "T7":
        rows = cli(bin_dir, "reviews", "com.spotify.music", "-lang", "ru", "-sort", "helpfulness", "-limit", "3000", "-fields", "score")
        return {"value": sum(1 for r in rows if r["score"] == 1), "n": len(rows)}
    if task == "T8":
        rows = cli(bin_dir, "reviews", "com.whatsapp", "-langs", "all", "-sort", "helpfulness", "-limit", "100", "-fields", "score")
        return {"value": round(statistics.mean(r["score"] for r in rows), 2), "n": len(rows)}
    if task == "T9":
        rows = cli(bin_dir, "reviews", "com.instagram.android", "-lang", "en", "-sort", "helpfulness", "-limit", "2000", "-fields", "untrusted_text")
        hits = sum(1 for r in rows if re.search(r"\bads?\b", r.get("untrusted_text", ""), re.I))
        return {"value": round(100 * hits / len(rows), 1), "n": len(rows)}
    raise KeyError(task)


def answer(text):
    m = re.findall(r"ANSWER:\s*`?([^`\n]+)`?", text or "")
    return m[-1].strip().strip("*").strip() if m else None


def grade(task, ans, truths):
    if ans is None:
        return False
    nums = re.findall(r"-?\d+(?:[.,]\d+)?", ans.replace(",", "") if task == "T7" else ans)
    if not nums:
        return False
    x = float(nums[0].replace(",", "."))
    for t in truths:
        if task == "T7" and abs(x - t) <= max(1, 0.03 * t):
            return True
        if task == "T8" and abs(x - t) <= 0.02 + 1e-9:
            return True
        if task == "T9" and abs(x - t) <= 0.5 + 1e-9:
            return True
    return False


def run_one(arm, task, rep, bin_dir, out_dir, truths):
    work = tempfile.mkdtemp(prefix=f"route2-{arm}{task}{rep}-")
    if arm == "D":
        os.makedirs(os.path.join(work, ".claude", "skills", "gpscrape"))
        shutil.copy(SKILL, os.path.join(work, ".claude", "skills", "gpscrape", "SKILL.md"))
    cfg = os.path.join(work, ".mcp-eval.json")
    with open(cfg, "w") as f:
        json.dump({"mcpServers": {"gpscrape": {"command": os.path.join(bin_dir, "gpscrape-mcp")}}}, f)
    path = "/usr/bin:/bin:/usr/sbin:/sbin:/opt/homebrew/bin"
    if arm != "A":
        path = bin_dir + ":" + path
    else:
        # gpscrape-mcp is started by absolute path; the CLI must not be
        # reachable, so the bin dir is left off PATH entirely.
        pass
    cmd = [CLAUDE, "-p", "--model", "sonnet", "--setting-sources", "project", "--strict-mcp-config", "--mcp-config", cfg,
           "--settings", json.dumps(SANDBOX), "--no-session-persistence", "--output-format", "stream-json", "--verbose",
           "--max-turns", "40", "--max-budget-usd", "3",
           "--allowedTools", "mcp__gpscrape", "Edit(/**)",
           "--disallowedTools", "WebFetch", "WebSearch",
           "--", TASKS[task] + END]
    t0 = time.time()
    try:
        p = subprocess.run(cmd, cwd=work, capture_output=True, text=True, timeout=1200, stdin=subprocess.DEVNULL,
                           env=dict(os.environ, PATH=path))
        stream, timed_out = p.stdout, False
    except subprocess.TimeoutExpired as e:
        stream = e.stdout.decode() if isinstance(e.stdout, bytes) else (e.stdout or "")
        timed_out = True
    wall = time.time() - t0
    with open(os.path.join(out_dir, f"{arm}-{task}-{rep}.jsonl"), "w") as f:
        f.write(stream)

    uses, results, final = {}, {}, {}
    for line in stream.splitlines():
        try:
            ev = json.loads(line)
        except json.JSONDecodeError:
            continue
        msg = ev.get("message", {}) if isinstance(ev.get("message"), dict) else {}
        for block in msg.get("content", []) if isinstance(msg.get("content"), list) else []:
            if block.get("type") == "tool_use":
                uses[block["id"]] = block
            elif block.get("type") == "tool_result":
                results[block.get("tool_use_id")] = bool(block.get("is_error"))
        if ev.get("type") == "result":
            final = ev
    mcp_calls = sum(1 for u in uses.values() if u["name"].startswith("mcp__gpscrape__"))
    cli_data_ok, cli_data_err, installs, skill_read = 0, 0, [], False
    for uid, u in uses.items():
        if u["name"] == "Bash":
            c = u.get("input", {}).get("command", "")
            if CLI_DATA.search(c):
                if results.get(uid) is False:
                    cli_data_ok += 1
                else:
                    cli_data_err += 1
            if INSTALL.search(c):
                installs.append(c)
        elif u["name"] == "Skill" and "gpscrape" in json.dumps(u.get("input", {})):
            skill_read = True
        elif u["name"] == "Read" and u.get("input", {}).get("file_path", "").endswith("SKILL.md"):
            skill_read = True
    ans = answer(final.get("result"))
    shutil.rmtree(work, ignore_errors=True)
    return {
        "arm": arm, "task": task, "rep": rep, "answer": ans, "truth": truths,
        "correct": grade(task, ans, truths),
        "mcp_calls": mcp_calls, "cli_data_ok": cli_data_ok, "cli_data_err": cli_data_err,
        "skill_read": skill_read, "installs": installs,
        "cost": final.get("total_cost_usd"), "turns": final.get("num_turns"), "wall_s": round(wall, 1),
        "subtype": final.get("subtype"), "is_error": final.get("is_error"), "timed_out": timed_out,
    }


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--bin", required=True)
    ap.add_argument("--out", required=True)
    ap.add_argument("--workers", type=int, default=2)
    ap.add_argument("--only")
    a = ap.parse_args()
    os.makedirs(a.out, exist_ok=True)
    tasks = [a.only.split(":")[1]] if a.only else list(TASKS)
    before = {t: truth(a.bin, t) for t in tasks}
    print(json.dumps({"truth_before": before}), flush=True)
    jobs = [(arm, t, r) for arm in ARMS for t in tasks for r in range(1, REPS + 1)]
    if a.only:
        jobs = [(a.only.split(":")[0], tasks[0], 1)]
    random.Random(SEED).shuffle(jobs)
    res = []
    with cf.ThreadPoolExecutor(a.workers) as ex:
        futs = {ex.submit(run_one, *j, a.bin, a.out, [before[j[1]]["value"]]): j for j in jobs}
        for fu in cf.as_completed(futs):
            j = futs[fu]
            try:
                r = fu.result()
            except Exception as e:
                r = {"arm": j[0], "task": j[1], "rep": j[2], "harness_error": str(e)}
            res.append(r)
            print(json.dumps({k: r.get(k) for k in ("arm", "task", "rep", "correct", "answer", "mcp_calls", "cli_data_ok", "skill_read", "installs", "cost", "subtype", "harness_error")}), flush=True)
    after = {t: truth(a.bin, t) for t in tasks}
    print(json.dumps({"truth_after": after}), flush=True)
    # Regrade against both truths where one moved, as the plan says.
    for r in res:
        t = r.get("task")
        if t and "answer" in r and before[t]["value"] != after[t]["value"]:
            r["truth"] = [before[t]["value"], after[t]["value"]]
            r["drift"] = True
            r["correct"] = grade(t, r["answer"], r["truth"])
    with open(os.path.join(a.out, "results.jsonl"), "w") as f:
        f.write(json.dumps({"truth_before": before, "truth_after": after}) + "\n")
        for r in res:
            f.write(json.dumps(r) + "\n")


if __name__ == "__main__":
    main()
