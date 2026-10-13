#!/usr/bin/env python3
"""Scan the exact Git candidate set (or index) without printing secret values."""

import argparse
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys
import tempfile


ROOT = Path(__file__).resolve().parents[1]


def git(args, env):
    return subprocess.check_output(
        ["git", "-c", "core.excludesFile=/dev/null", *args],
        cwd=ROOT, env=env, stderr=subprocess.PIPE,
    )


def protected(path):
    parts = Path(path).parts
    name = Path(path).name.lower()
    if any(part in {".local", ".secrets", "keystores"} for part in parts):
        return True
    if (name == ".env" or name.startswith(".env.")) and name != ".env.example":
        return True
    if name in {".envrc", "id_rsa", "id_ed25519", "server-password",
                "private_key.txt", "mnemonic.txt", "seed-phrase.txt", "dump.rdb", "appendonly.aof"}:
        return True
    return name.endswith((".keystore", ".pem", ".key", ".p12", ".pfx",
                          "-private-key.txt", "-password", ".dump", ".sql.gz",
                          ".db", ".sqlite", ".sqlite3"))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--staged", action="store_true",
                        help="Scan index contents, including unchanged tracked files; exclude unstaged edits.")
    args = parser.parse_args()
    scanner = shutil.which("gitleaks")
    if not scanner:
        print("ERROR: install gitleaks 8.25+ before running this check.", file=sys.stderr)
        return 2
    env = os.environ.copy()
    for key in ("GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE"):
        env.pop(key, None)
    try:
        git(["rev-parse", "--show-toplevel"], env)
        repository = True
    except subprocess.CalledProcessError:
        repository = False
    if args.staged and not repository:
        print("ERROR: --staged requires an existing Git repository.", file=sys.stderr)
        return 2

    findings = []
    with tempfile.TemporaryDirectory(prefix="tokenswoop-secret-check-") as temp:
        temp = Path(temp)
        if not repository:
            metadata = temp / "empty.git"
            subprocess.run(["git", "init", "--bare", "--quiet", str(metadata)],
                           check=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
            env.update(GIT_DIR=str(metadata), GIT_WORK_TREE=str(ROOT))
        if git(["ls-files", "--unmerged", "-z", "--", "."], env):
            print("ERROR: resolve merge conflicts before scanning.", file=sys.stderr)
            return 2
        options = ["--cached"] if args.staged else ["--cached", "--others", "--exclude-standard"]
        paths = sorted(set(os.fsdecode(p) for p in
                           git(["ls-files", *options, "-z", "--", "."], env).split(b"\0") if p))
        snapshot = temp / "candidate"
        snapshot.mkdir()
        scanned = 0
        for rel in paths:
            if Path(rel).is_absolute() or ".." in Path(rel).parts:
                raise RuntimeError("Unexpected path outside the project")
            source = ROOT / rel
            if protected(rel):
                findings.append({"rule": "protected-local-file", "file": rel, "line": 0})
            if args.staged:
                entry = git(["ls-files", "--stage", "--", rel], env).decode().split(" ", 1)[0]
                if entry not in {"100644", "100755"}:
                    findings.append({"rule": "review-symlink-or-submodule", "file": rel, "line": 0})
                    continue
                # :./ resolves paths from this project, also when it is a subdirectory of a repo.
                data = git(["show", ":./" + rel], env)
            else:
                if source.is_symlink():
                    findings.append({"rule": "review-symlink", "file": rel, "line": 0})
                    continue
                if not source.is_file():
                    continue
                data = source.read_bytes()
            if re.fullmatch(rb"(?:0x)?[0-9a-fA-F]{64}", data.strip()):
                findings.append({"rule": "review-standalone-wallet-key", "file": rel, "line": 1})
            if rel.lower().endswith(".json"):
                try:
                    record = json.loads(data)
                    crypto = record.get("crypto", record.get("Crypto")) if isinstance(record, dict) else None
                    if isinstance(crypto, dict) and record.get("version") == 3 and "ciphertext" in crypto and "kdf" in crypto:
                        findings.append({"rule": "encrypted-wallet-keystore", "file": rel, "line": 1})
                except (ValueError, UnicodeDecodeError):
                    pass
            target = snapshot / rel
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_bytes(data)
            scanned += 1

        raw_report = temp / "gitleaks.json"
        ignore = temp / "empty.ignore"
        ignore.write_text("")
        run = subprocess.run(
            [scanner, "dir", str(snapshot), "--config", str(ROOT / ".gitleaks.toml"),
             "--no-banner", "--no-color", "--redact=100", "--ignore-gitleaks-allow",
             "--gitleaks-ignore-path", str(ignore), "--max-archive-depth", "3",
             "--report-format", "json", "--report-path", str(raw_report)],
            cwd=ROOT, capture_output=True, text=True,
        )
        if run.returncode not in (0, 1) or not raw_report.exists():
            print("ERROR: secret scanner failed; check the gitleaks version and configuration.", file=sys.stderr)
            return 2
        for item in json.loads(raw_report.read_text()):
            name = item["File"]
            if name.startswith(str(snapshot) + os.sep):
                name = str(Path(name).relative_to(snapshot))
            findings.append({"rule": item["RuleID"], "file": name, "line": item["StartLine"]})
        if run.returncode == 1 and not findings:
            print("ERROR: scanner returned an unsuccessful result without findings.", file=sys.stderr)
            return 2

    report = {
        "mode": "index" if args.staged else "working-tree-candidates",
        "existing_repository": repository,
        "scanned_files": scanned,
        "findings": findings,
    }
    report_dir = ROOT / ".local/security-audit"
    report_dir.mkdir(parents=True, exist_ok=True)
    (report_dir / "latest.json").write_text(json.dumps(report, indent=2) + "\n")
    (report_dir / "candidate-files.json").write_text(json.dumps(paths, indent=2) + "\n")
    print(f"Scanned {scanned} files ({report['mode']}).")
    if not repository:
        print("No Git repository: evaluated a first commit using this project's .gitignore.")
    for item in findings:
        print(f"BLOCKED {item['file']}:{item['line']} [{item['rule']}]")
    if findings:
        print(f"FAIL: {len(findings)} finding(s). Secret values were not printed.")
        return 1
    print("PASS: no findings in the candidate snapshot. Git history was not scanned.")
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except (OSError, RuntimeError, subprocess.SubprocessError, ValueError):
        print("ERROR: unable to complete the secret check; no clean result was issued.", file=sys.stderr)
        sys.exit(2)
