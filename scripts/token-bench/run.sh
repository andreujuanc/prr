#!/usr/bin/env bash
set -euo pipefail

# Token bench: compare token usage of claude -p audits under three
# system-prompt variants (none / full 12 rules / distilled rules).
#
# Env vars:
#   ITERATIONS   number of runs per condition (default 5)
#   CONDITIONS   space-separated list (default "none full distilled")
#   MODEL        claude model (default claude-opus-4-7)

BENCH_DIR="$(cd "$(dirname "$0")" && pwd)"
FIXTURE="$BENCH_DIR/fixtures/audit-target"
RESULTS="$BENCH_DIR/results"
mkdir -p "$RESULTS/outputs"

ITERATIONS="${ITERATIONS:-5}"
CONDITIONS="${CONDITIONS:-none full distilled}"
MODEL="${MODEL:-claude-opus-4-7}"

CSV="$RESULTS/runs.csv"
if [[ ! -f "$CSV" ]]; then
  echo "condition,iteration,input_tokens,output_tokens,cache_read,cache_create,total_cost_usd,caught,output_path" > "$CSV"
fi

# score_output FILE  -> echoes integer (0..5) of issues caught
score_output() {
  local out="$1"
  local n=0
  # ISSUE 1: SQL injection in vulnerableQuery
  if grep -qiE 'vulnerableQuery|sql.{0,15}injection|injection' "$out"; then n=$((n+1)); fi
  # ISSUE 2: swallowed errors in loadConfig
  if grep -qi 'loadConfig' "$out" || \
     { grep -qiE 'swallow|ignor' "$out" && grep -qi 'err' "$out"; }; then n=$((n+1)); fi
  # ISSUE 3: nil deref via lookupCached / processUser / HandleGetUser
  if { grep -qiE 'processUser|lookupCached|HandleGetUser' "$out" && grep -qi 'nil' "$out"; } || \
     grep -qiE 'nil.{0,10}(deref|pointer|panic)' "$out"; then n=$((n+1)); fi
  # ISSUE 4: over-abstracted single-use Stringer interface
  if grep -qiE 'Stringer|userLabel|over.{0,5}abstract|premature.{0,5}abstract|unnecessary.{0,15}interface|single.{0,5}use.{0,15}interface' "$out"; then n=$((n+1)); fi
  # ISSUE 5: dead code unusedHelper
  if grep -qiE 'unusedHelper|dead.{0,5}code|unused.{0,15}func|never.{0,5}called|never.{0,5}used' "$out"; then n=$((n+1)); fi
  echo "$n"
}

run_one() {
  local cond="$1" iter="$2"
  local tmpdir
  tmpdir="$(mktemp -d "${TMPDIR:-/tmp}/token-bench.XXXXXX")"
  # Clean up on any exit path (success, error, Ctrl+C, SIGTERM). The
  # trap is cleared right before normal return so callers can rerun
  # without inheriting state.
  trap 'rm -rf "$tmpdir"' EXIT INT TERM
  cp -r "$FIXTURE/." "$tmpdir/"
  local raw_path="$RESULTS/outputs/${cond}-${iter}.jsonl"
  local out_path="$RESULTS/outputs/${cond}-${iter}.md"

  local sys_prompt
  sys_prompt="$(cat "$BENCH_DIR/prompts/${cond}.txt")"

  local args=(
    -p
    --output-format stream-json
    --input-format text
    --verbose
    --model "$MODEL"
    --permission-mode bypassPermissions
    --allowed-tools "Read Grep Glob LS"
    --no-session-persistence
  )
  if [[ -n "$sys_prompt" ]]; then
    args+=( --append-system-prompt "$sys_prompt" )
  fi

  (
    cd "$tmpdir"
    claude "${args[@]}" < "$BENCH_DIR/prompts/user.txt"
  ) > "$raw_path"

  local result_json
  result_json="$(grep '"type":"result"' "$raw_path" | tail -1 || true)"
  if [[ -z "$result_json" ]]; then
    echo "  WARN: no result event for $cond-$iter (see $raw_path)" >&2
    rm -rf "$tmpdir"
    trap - EXIT INT TERM
    return 1
  fi

  local input_tok output_tok cache_read cache_create cost text
  input_tok=$(echo "$result_json"  | jq -r '.usage.input_tokens // 0')
  output_tok=$(echo "$result_json" | jq -r '.usage.output_tokens // 0')
  cache_read=$(echo "$result_json" | jq -r '.usage.cache_read_input_tokens // 0')
  cache_create=$(echo "$result_json" | jq -r '.usage.cache_creation_input_tokens // 0')
  cost=$(echo "$result_json"       | jq -r '.total_cost_usd // 0')
  text=$(echo "$result_json"       | jq -r '.result // ""')

  printf "%s\n" "$text" > "$out_path"
  local caught
  caught="$(score_output "$out_path")"

  echo "$cond,$iter,$input_tok,$output_tok,$cache_read,$cache_create,$cost,$caught,$out_path" >> "$CSV"
  printf "  [%-9s #%d] in=%-6s out=%-6s cache_r=%-6s cost=\$%-8s caught=%s/5\n" \
    "$cond" "$iter" "$input_tok" "$output_tok" "$cache_read" "$cost" "$caught"

  rm -rf "$tmpdir"
  trap - EXIT INT TERM
}

for cond in $CONDITIONS; do
  if [[ ! -f "$BENCH_DIR/prompts/${cond}.txt" ]]; then
    echo "skip: no prompts/${cond}.txt" >&2
    continue
  fi
  for i in $(seq 1 "$ITERATIONS"); do
    echo "== $cond iteration $i =="
    run_one "$cond" "$i" || true
  done
done

echo ""
echo "=== SUMMARY ==="
python3 - "$CSV" <<'PY'
import csv, statistics, sys
from collections import defaultdict

rows = defaultdict(list)
with open(sys.argv[1]) as f:
    for row in csv.DictReader(f):
        rows[row["condition"]].append(row)

hdr = f"{'cond':<11} {'in_avg':>8} {'out_avg':>8} {'total':>8} {'cost_avg':>9} {'caught':>7} {'out_sd':>7} {'tok/find':>10}"
print(hdr)
print("-" * len(hdr))
for cond in ("none", "full", "distilled"):
    items = rows.get(cond, [])
    if not items: continue
    ins   = [int(x["input_tokens"])  for x in items]
    outs  = [int(x["output_tokens"]) for x in items]
    costs = [float(x["total_cost_usd"]) for x in items]
    caught = [int(x["caught"]) for x in items]
    totals = [a+b for a,b in zip(ins, outs)]
    sd = (statistics.stdev(outs) if len(outs) > 1 else 0.0)
    mean_caught = statistics.mean(caught)
    tpf = (statistics.mean(outs) / mean_caught) if mean_caught > 0 else float('inf')
    print(f"{cond:<11} {statistics.mean(ins):>8.0f} {statistics.mean(outs):>8.0f} {statistics.mean(totals):>8.0f} {statistics.mean(costs):>9.4f} {mean_caught:>7.2f} {sd:>7.0f} {tpf:>10.0f}")
PY
