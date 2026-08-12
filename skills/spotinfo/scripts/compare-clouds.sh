#!/usr/bin/env bash
# Compare the same requirement across AWS, GCP and Azure and print one table.
#
# Machine naming differs per cloud, so there is no single cross-cloud query.
# This runs one `spotinfo recommend` per cloud with identical constraints and
# merges the results, which is the multi-step task worth not rewriting.
#
# Usage:
#   compare-clouds.sh --min-vcpu 4 --min-memory-gib 16 [--architecture x86_64] [--top 3] [--offline]
#
# Any extra flags are passed through to spotinfo unchanged.
#
# Requires: spotinfo on PATH, jq.

set -euo pipefail

command -v spotinfo >/dev/null || { echo "compare-clouds: spotinfo is not on PATH" >&2; exit 127; }
command -v jq >/dev/null       || { echo "compare-clouds: jq is not on PATH" >&2; exit 127; }

architecture=x86_64
top=3
passthrough=()
have_vcpu=false
have_memory=false

while [ $# -gt 0 ]; do
  case "$1" in
    --architecture) architecture="$2"; shift 2 ;;
    --top)          top="$2";          shift 2 ;;
    --min-vcpu)       have_vcpu=true;   passthrough+=("$1" "$2"); shift 2 ;;
    --min-memory-gib) have_memory=true; passthrough+=("$1" "$2"); shift 2 ;;
    *)              passthrough+=("$1"); shift ;;
  esac
done

if [ "$have_vcpu" = false ] || [ "$have_memory" = false ]; then
  echo "compare-clouds: --min-vcpu and --min-memory-gib are both required" >&2
  echo "usage: compare-clouds.sh --min-vcpu 4 --min-memory-gib 16 [--architecture x86_64] [--top 3]" >&2
  exit 2
fi

printf '%-7s %-22s %-18s %12s %8s  %s\n' CLOUD MACHINE REGION USD/HOUR SAVINGS RISK
printf '%-7s %-22s %-18s %12s %8s  %s\n' ------ ------- ------ -------- ------- ----

answered=0
for cloud in aws gcp azure; do
  # A cloud that refuses the question is reported, not fatal: the point of the
  # comparison is the clouds that can answer it.
  if ! out=$(spotinfo recommend --cloud "$cloud" --output json \
               --architecture "$architecture" --top "$top" \
               "${passthrough[@]}" 2>/tmp/compare-clouds.err); then
    printf '%-7s %s\n' "$cloud" "refused: $(head -1 /tmp/compare-clouds.err | sed 's/^spotinfo: //')"
    continue
  fi
  answered=$((answered + 1))
  jq -r --arg cloud "$cloud" '
    .recommendations[] |
    [ $cloud, .machine, .region,
      (.spot_usd_per_hour // "n/a"),
      ((.savings_percent | tostring) + "%"),
      (if .risk.status == "available" then .risk.label else "not published" end)
    ] | @tsv' <<<"$out" \
  | while IFS=$'\t' read -r c m r p s k; do
      printf '%-7s %-22s %-18s %12s %8s  %s\n' "$c" "$m" "$r" "$p" "$s" "$k"
    done
done

rm -f /tmp/compare-clouds.err

if [ "$answered" -eq 0 ]; then
  echo "compare-clouds: no cloud answered this requirement" >&2
  exit 1
fi

cat <<'NOTE'

Only AWS publishes an interruption figure. "not published" means the vendor
does not measure it, not that the risk is low.
NOTE
