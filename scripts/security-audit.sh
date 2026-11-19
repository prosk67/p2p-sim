#!/usr/bin/env bash
# Runs the full security audit suite: secrets, dependency CVEs, SAST,
# Dockerfile lint and (optionally) Docker image CVE scans. Results are
# written as JSON to an output directory; use --report to also produce a
# dated Markdown summary in docs/.
#
#   scripts/security-audit.sh              # full audit (incl. image builds)
#   scripts/security-audit.sh --quick      # skip image build/scan (fast)
#   scripts/security-audit.sh --report     # audit + docs/security-audit-summary-<date>.md
#
# The hand-maintained findings and dispositions live in docs/security-audit.md.
#
# Prerequisites:
#   - trivy, bandit, pip-audit on PATH or in .venv/ (.venv/bin/pip install bandit pip-audit)
#   - npm (and the Node.js next to it) for the web audit and --report
#   - Docker for gitleaks, hadolint, semgrep and image scans
#
# Environment:
#   SECURITY_AUDIT_OUT   results directory (default: results/security-audit, git-ignored)
#   GITLEAKS_IMAGE, HADOLINT_IMAGE, SEMGREP_IMAGE   scanner images (pinned below)
set -uo pipefail

cd "$(dirname "$0")/.."
ROOT="$(pwd)"

QUICK=0
REPORT=0
for arg in "$@"; do
  case "$arg" in
    --quick)  QUICK=1 ;;
    --report) REPORT=1 ;;
    *) echo "unknown option: $arg (use --quick, --report)"; exit 2 ;;
  esac
done

OUT="${SECURITY_AUDIT_OUT:-$ROOT/results/security-audit}"
mkdir -p "$OUT"
OUT="$(cd "$OUT" && pwd)"

# Pinned so two runs of the audit use the same rules; bump deliberately.
GITLEAKS_IMAGE="${GITLEAKS_IMAGE:-zricethezav/gitleaks:v8.24.0}"
HADOLINT_IMAGE="${HADOLINT_IMAGE:-hadolint/hadolint:v2.12.0}"
SEMGREP_IMAGE="${SEMGREP_IMAGE:-semgrep/semgrep:1.100.0}"
# Containers write into $OUT as the calling user, not root.
DOCKER_USER=(--user "$(id -u):$(id -g)")
FAILED=0
note() { echo "[audit] $*"; }

# Locate trivy: PATH first, then the project venv (installed by the audit
# setup so no global tool pollution).
TRIVY="$(command -v trivy || true)"
[[ -z "$TRIVY" && -x .venv/bin/trivy ]] && TRIVY=.venv/bin/trivy
BANDIT="$(command -v bandit || true)"
[[ -z "$BANDIT" && -x .venv/bin/bandit ]] && BANDIT=.venv/bin/bandit
PIPAUDIT="$(command -v pip-audit || true)"
[[ -z "$PIPAUDIT" && -x .venv/bin/pip-audit ]] && PIPAUDIT=.venv/bin/pip-audit

run() { # run <name> <cmd...>
  local name="$1"; shift
  note "run $name"
  if "$@" >"$OUT/$name.log" 2>&1; then
    note "  ok: $name"
  else
    note "  FINDINGS or error: $name (see $OUT/$name.log)"
    FAILED=$((FAILED + 1))
  fi
}

# ── Secrets (full git history) ───────────────────────────────────────────
if command -v docker >/dev/null; then
  run gitleaks docker run --rm "${DOCKER_USER[@]}" -v "$ROOT:/repo:ro" -v "$OUT:/out" \
    "$GITLEAKS_IMAGE" detect --source /repo \
    --report-path /out/gitleaks.json --report-format json --no-banner
else
  note "SKIP gitleaks: no docker"
fi

# ── Dependency CVEs ──────────────────────────────────────────────────────
if [[ -n "$TRIVY" ]]; then
  for t in gateway:gateway/go.mod node:node/go.mod sim:sim/requirements-dev.txt web:web/package-lock.json; do
    name="${t%%:*}"; path="${t#*:}"
    run "deps-$name" "$TRIVY" fs --scanners vuln --skip-dirs "node_modules,.venv" \
      --format json --output "$OUT/deps-$name.json" "$path"
  done
else
  note "SKIP trivy dependency scans: not installed"
fi

# Audit the declared requirements, not whatever environment happens to be active.
if [[ -n "$PIPAUDIT" ]]; then
  run pip-audit "$PIPAUDIT" -r sim/requirements.txt -r sim/requirements-dev.txt \
    --format json --output "$OUT/pip-audit.json"
else
  note "SKIP pip-audit: not installed"
fi
if command -v npm >/dev/null; then
  run npm-audit bash -c "cd web && npm audit --json > '$OUT/npm-audit.json'"
else
  note "SKIP npm audit: npm not installed"
fi

# ── SAST ─────────────────────────────────────────────────────────────────
# B101 (assert) only fires in pytest files; skipping it lets real findings stand out.
if [[ -n "$BANDIT" ]]; then
  run bandit "$BANDIT" -r sim --skip B101 -f json -o "$OUT/bandit.json" -q
else
  note "SKIP bandit: not installed"
fi
[[ -n "$TRIVY" ]] && run trivy-config "$TRIVY" config \
  --skip-dirs ".venv,node_modules,.git,dist" \
  --format json --output "$OUT/config.json" .

if command -v docker >/dev/null; then
  run semgrep docker run --rm "${DOCKER_USER[@]}" -e HOME=/tmp -v "$ROOT:/src:ro" -v "$OUT:/out" \
    "$SEMGREP_IMAGE" semgrep --config p/golang --config p/typescript \
    --config p/python --json --output /out/semgrep.json --metrics=off
else
  note "SKIP semgrep: no docker"
fi

# ── Dockerfile lint ──────────────────────────────────────────────────────
if command -v docker >/dev/null; then
  for f in gateway/Dockerfile node/Dockerfile web/Dockerfile; do
    note "hadolint $f"
    docker run --rm -i "$HADOLINT_IMAGE" hadolint - <"$f" >"$OUT/hadolint-$(echo "$f" | tr / -).log" 2>&1 \
      || FAILED=$((FAILED + 1))
  done
else
  note "SKIP hadolint: no docker"
fi

# ── Docker image CVEs (slow) ─────────────────────────────────────────────
if [[ $QUICK -eq 0 ]]; then
  if command -v docker >/dev/null; then
    docker build -f node/Dockerfile    -t p2p-sim-node:latest    . >"$OUT/build-node.log"    2>&1 \
      || { note "image build failed: node"; FAILED=$((FAILED + 1)); }
    docker build -f gateway/Dockerfile -t p2p-sim-gateway:latest . >"$OUT/build-gateway.log" 2>&1 \
      || { note "image build failed: gateway"; FAILED=$((FAILED + 1)); }
    docker build -t p2p-sim-web:latest web >"$OUT/build-web.log" 2>&1 \
      || { note "image build failed: web"; FAILED=$((FAILED + 1)); }
    if [[ -n "$TRIVY" ]]; then
      for img in p2p-sim-node p2p-sim-gateway p2p-sim-web; do
        run "image-$img" "$TRIVY" image --skip-dirs "/var/lib/dbus" \
          --format json --output "$OUT/image-$img.json" "$img:latest"
      done
    fi
  else
    note "SKIP image scans: no docker"
  fi
else
  note "SKIP image scans (--quick)"
fi

note "done; results in $OUT; $FAILED scan(s) reported findings or errors"

# ── Optional dated report ────────────────────────────────────────────────
if [[ $REPORT -eq 1 ]]; then
  # The detailed hand-maintained audit report lives in docs/; the script
  # writes its compact machine-generated summary alongside it.
  REPORT_FILE="docs/security-audit-summary-$(date +%Y-%m-%d).md"
  # Use the Node.js that ships with npm. A bare `node` can resolve to the
  # project's Go node binary (bin/node) when that is earlier on PATH.
  NODEJS=""
  for candidate in "$(dirname "$(command -v npm 2>/dev/null || echo /nonexistent)")/node" $(type -ap node nodejs); do
    if [[ -x "$candidate" ]] && "$candidate" -e 'process.exit(0)' >/dev/null 2>&1; then
      NODEJS="$candidate"; break
    fi
  done
  if [[ -z "$NODEJS" ]]; then
    note "cannot write report: no Node.js found"
    exit $((FAILED + 1))
  fi
  "$NODEJS" -e '
    const fs = require("fs"), path = require("path");
    const out = process.argv[1], file = process.argv[2];
    const j = f => { try { return JSON.parse(fs.readFileSync(path.join(out, f))); } catch { return null; } };
    const sev = r => { const c = {}; (r || []).forEach(v => { const s = (v.Severity || v.sev || "UNKNOWN").toUpperCase(); c[s] = (c[s] || 0) + 1; }); return c; };
    const fmt = c => ["CRITICAL","HIGH","MEDIUM","LOW","UNKNOWN"].filter(s => c[s]).map(s => `${c[s]} ${s}`).join(", ") || "clean";
    let deps = [];
    for (const n of ["gateway","node","sim","web"]) {
      const r = j(`deps-${n}.json`);
      if (r) deps.push([n, fmt(sev((r.Results || []).flatMap(x => x.Vulnerabilities || [])))]);
    }
    // pip-audit JSON: {"dependencies":[{"name","version","vulns":[...]}]} (no severities).
    const pa = j("pip-audit.json");
    if (pa) {
      const vulns = (pa.dependencies || []).reduce((n, d) => n + (d.vulns || []).length, 0);
      deps.push(["sim requirements (pip-audit)", vulns ? `${vulns} known vulnerabilit${vulns === 1 ? "y" : "ies"}` : "clean"]);
    }
    const na = j("npm-audit.json");
    if (na) {
      const v = Object.values(na.vulnerabilities || {});
      deps.push(["web (npm audit)", fmt(sev(v.map(x => ({sev: (x.severity || "unknown").toUpperCase()}))))]);
    }
    let imgs = [];
    for (const n of ["p2p-sim-node","p2p-sim-gateway","p2p-sim-web"]) {
      const r = j(`image-${n}.json`);
      if (r) imgs.push([n, fmt(sev((r.Results || []).flatMap(x => x.Vulnerabilities || [])))]);
    }
    const b = j("bandit.json");
    const bm = b && b.metrics && b.metrics._totals ? b.metrics._totals : {};
    const s = j("semgrep.json");
    const g = j("gitleaks.json");
    const cf = j("config.json");
    const hado = [];
    for (const f of fs.readdirSync(out).filter(x => x.startsWith("hadolint-"))) {
      const lines = fs.readFileSync(path.join(out, f), "utf8").trim().split("\n").filter(Boolean);
      hado.push([f.replace("hadolint-","").replace(/-/g,"/").replace(".log",""), String(lines.length)]);
    }
    const row = (a, b2) => `| ${a} | ${b2} |\n`;
    let md = `# Security Audit — ${new Date().toISOString().slice(0,10)}\n\n` +
      "Auto-generated by scripts/security-audit.sh --report. Raw JSON results are in the\n" +
      "audit output directory; see docs/security-audit.md for findings, context\n" +
      "and dispositions.\n\n## Dependency CVEs\n\n| Component | Findings |\n|---|---|\n";
    deps.forEach(d => md += row(d[0], d[1]));
    md += "\n## Docker image CVEs\n\n| Image | Findings |\n|---|---|\n";
    imgs.forEach(d => md += row(d[0], d[1]));
    md += "\n## SAST / secrets / config\n\n| Check | Result |\n|---|---|\n";
    md += row("bandit (B101 skipped)", `${bm["SEVERITY.HIGH"] || 0} HIGH, ${bm["SEVERITY.MEDIUM"] || 0} MEDIUM, ${bm["SEVERITY.LOW"] || 0} LOW (${bm.loc || 0} loc)`);
    md += row("semgrep", `${s && s.results ? s.results.length : "?"} findings, ${s && s.errors ? s.errors.length : "?"} errors`);
    md += row("gitleaks", `${g && g.length ? g.length : 0} leaks`);
    md += row("trivy config (Dockerfiles)", `${(cf && cf.Results || []).reduce((a, r) => a + ((r.Misconfigurations || []).length), 0)} misconfigs`);
    hado.forEach(h => md += row(`hadolint ${h[0]}`, h[1]));
    fs.writeFileSync(file, md);
    console.log(`[audit] report written: ${file}`);
  ' "$OUT" "$REPORT_FILE"
  [[ -f "$REPORT_FILE" ]] || FAILED=$((FAILED + 1))
fi

exit $FAILED
