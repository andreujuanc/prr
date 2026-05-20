#!/usr/bin/env bash
#
# Fetches the current Gemini model list from the API and updates
# internal/config/known_models.go with text-capable 3.1 models.
#
# Usage:
#   ./scripts/update-models.sh
#   # Reads the Gemini API key from ~/.config/prr/config.json
#
# The script filters out non-text models (image, TTS, computer-use, robotics,
# embeddings, custom-tools) and only keeps models that support generateContent
# with >= 100K input context.
#
set -euo pipefail

# Read Gemini API key from config.json
CONFIG_FILE="${HOME}/.config/prr/config.json"
if [ ! -f "$CONFIG_FILE" ]; then
  echo "error: $CONFIG_FILE not found — configure the app first" >&2
  exit 1
fi

KEY=$(python3 -c "
import json, sys
try:
    cfg = json.load(open('$CONFIG_FILE'))
    print(cfg.get('providers', {}).get('gemini', {}).get('api_key', ''))
except Exception:
    pass
")

if [ -z "$KEY" ]; then
  echo "error: no gemini API key found in $CONFIG_FILE" >&2
  echo "  Add it under providers.gemini.api_key" >&2
  exit 1
fi

API_URL="https://generativelanguage.googleapis.com/v1beta/models"
TARGET="internal/config/known_models.go"

if [ ! -f "$TARGET" ]; then
  echo "error: $TARGET not found — run from repo root" >&2
  exit 1
fi

echo "Fetching models from Gemini API..."

# Pass the API key via header rather than query parameter so it does
# not show up in process listings (`ps aux`) or shell history.
MODELS_JSON=$(curl -sf -H "x-goog-api-key: ${KEY}" "$API_URL") || {
  echo "error: failed to fetch models from API" >&2
  exit 1
}

# Generate the Go model entries from the API response
GEMINI_ENTRIES=$(echo "$MODELS_JSON" | python3 -c "
import json, sys

data = json.load(sys.stdin)
models = data.get('models', [])

# Filter: 3.1 family, text generation capable, large context
SKIP_KEYWORDS = ['image', 'tts', 'computer-use', 'robotics', 'embedding', 'customtools', 'live', 'research']

text_models = []
for m in models:
    mid = m['name'].replace('models/', '')
    if not (mid.startswith('gemini-3.') or mid.startswith('gemini-3-')):
        continue
    methods = m.get('supportedGenerationMethods', [])
    if 'generateContent' not in methods:
        continue
    if m.get('inputTokenLimit', 0) < 100000:
        continue
    if any(kw in mid for kw in SKIP_KEYWORDS):
        continue
    text_models.append({
        'id': mid,
        'display': m.get('displayName', mid),
        'input_limit': m.get('inputTokenLimit', 0),
        'output_limit': m.get('outputTokenLimit', 0),
    })

# Sort: pro before flash, stable before preview
def sort_key(m):
    mid = m['id']
    family = 0
    tier = 0 if '-pro' in mid else (1 if 'flash-lite' not in mid and 'flash' in mid else 2)
    preview = 1 if 'preview' in mid else 0
    return (family, tier, preview, mid)

text_models.sort(key=sort_key)

# Classify models for AOI and Review roles
# Pro models: review only. Flash/Flash-Lite: review + AOI.
# Lite-only models without thinking: AOI only.
for m in text_models:
    mid = m['id']
    is_pro = '-pro' in mid
    is_lite = 'lite' in mid
    is_31 = '3.1' in mid

    # Role assignment
    m['review'] = True  # all text models can review
    m['aoi'] = is_lite  # lite models are AOI-suitable
    m['thinking'] = is_pro or is_31

    # Speed estimate
    if is_lite:
        m['speed'] = 'fast'
    elif is_pro:
        m['speed'] = 'slow'
    else:
        m['speed'] = 'medium'

# Parse existing prices from known_models.go so re-running the script
# does not wipe manually-curated pricing data. Only Gemini entries are
# read (other providers are not regenerated here).
existing_prices = {}
try:
    with open('$TARGET', 'r') as f:
        src = f.read()
    # Match struct literals like:
    #   {ID: \"gemini-3.1-flash-lite\", ..., Provider: \"gemini\", ...,
    #       InputPricePer1M: 0.25, OutputPricePer1M: 1.50, Speed: \"fast\"}
    import re
    entry_re = re.compile(
        r'\{ID: \"([^\"]+)\"[^}]*?Provider: \"gemini\"[^}]*?'
        r'InputPricePer1M: ([\d.]+),\s*OutputPricePer1M: ([\d.]+)',
        re.DOTALL,
    )
    for mid, in_price, out_price in entry_re.findall(src):
        existing_prices[mid] = (float(in_price), float(out_price))
except FileNotFoundError:
    pass

# Print Go struct literals
missing_price = []
for m in text_models:
    flags = []
    if m['thinking']:
        flags.append('Thinking: true')
    if m['review']:
        flags.append('Review: true')
    if m['aoi']:
        flags.append('AOI: true')
    flags_str = ', '.join(flags)

    # Preserve existing prices when re-running; new models get 0.00 and
    # are reported below so the operator knows to fill them in.
    in_price, out_price = existing_prices.get(m['id'], (0.00, 0.00))
    if (in_price, out_price) == (0.00, 0.00) and m['id'] not in existing_prices:
        missing_price.append(m['id'])

    print(f'\t{{ID: \"{m[\"id\"]}\", Label: \"{m[\"display\"]}\", Provider: \"gemini\", {flags_str},')
    print(f'\t\tInputPricePer1M: {in_price:.2f}, OutputPricePer1M: {out_price:.2f}, Speed: \"{m[\"speed\"]}\"}},')

import sys as _sys
if missing_price:
    print('# MISSING_PRICE: ' + ','.join(missing_price), file=_sys.stderr)
")

if [ -z "$GEMINI_ENTRIES" ]; then
  echo "error: no text models found from API" >&2
  exit 1
fi

echo ""
echo "Found Gemini text models:"
echo "$GEMINI_ENTRIES"
echo ""

# Replace ONLY the Gemini section in known_models.go. Non-Gemini providers
# (Anthropic, OpenAI, GitHub Copilot, Claude Code) are preserved verbatim
# from the existing file.
python3 -c "
import re, sys

with open('$TARGET', 'r') as f:
    content = f.read()
gemini_entries = sys.stdin.read().rstrip('\n')

# Locate the Gemini section. The section starts at a line like
# \"\t// ── Gemini ...\" and continues (through any contiguous block of
# tab-indented comment lines and entries) until the next \"\t// ── \"
# section comment for a different provider.
start_re = re.compile(r'^\t// ── Gemini[^\n]*\n', re.MULTILINE)
m = start_re.search(content)
if not m:
    sys.exit('error: could not find \"// ── Gemini\" section marker in $TARGET')
section_start = m.start()

# Walk past any additional comment lines that follow the section header.
i = m.end()
while i < len(content) and content[i:i+3] == '\t//' and not content[i:i+8] == '\t// ── ':
    i = content.find('\n', i) + 1

# Find the next provider section comment after the Gemini block.
next_re = re.compile(r'^\t// ── (?!Gemini)', re.MULTILINE)
m2 = next_re.search(content, i)
if not m2:
    sys.exit('error: could not find provider section after Gemini in $TARGET')
section_end = m2.start()

new_header = (
    '\t// ── Gemini (auto-generated by scripts/update-models.sh) ──────────\n'
    '\t// Prices reflect Standard tier, ≤200K-context. Pro has a higher\n'
    '\t// tier above 200K (input \$4.00, output \$18.00) that this single-rate\n'
    '\t// field does not model.\n'
)
new_block = new_header + gemini_entries + '\n\n'

result = content[:section_start] + new_block + content[section_end:]

with open('$TARGET', 'w') as f:
    f.write(result)
" <<< "$GEMINI_ENTRIES"

echo "Updated Gemini section in $TARGET (other providers preserved)"
echo ""

# Format
if command -v gofmt &>/dev/null; then
  gofmt -w "$TARGET"
  echo "Formatted with gofmt"
fi
