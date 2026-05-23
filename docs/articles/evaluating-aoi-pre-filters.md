# Evaluating Recall-Biased AOI Pre-Filters: A Benchmark Methodology

## Abstract

Areas of Interest (AOIs) are recall-biased pre-filter annotations
emitted by an LLM scanner to mark code locations a deep reviewer
should examine. They are not defect assertions. PRR's original AOI
benchmark contradicted this contract by treating every emitted AOI
that did not match a hand-picked security ground truth as a "false
alarm," producing rankings that rewarded models which ignored the
prompt's surface-area rules. We rebuilt the benchmark around what
AOIs are actually for.

The new scoring classifies each emitted AOI as covered, aligned, or
hallucinated, and reports the average line offset between each AOI
and the nearest ground-truth entry. A CI-friendly sanity test
verifies that every ground-truth entry references real diff content;
it caught an off-by-one error that had been silently miscrediting
models. We tightened eighteen of the original twenty ground-truth
entries to bug-precise line ranges and added five entries surfaced
by inspecting one model's previously-uncredited output, raising
total entries to twenty-five.

A three-rep variance study across six models from three providers
shows reasoning-class models swing 8 to 24 coverage points across
identical prompts, while cheap deterministic models swing zero.
Forced thinking budgets degrade Gemini Flash Lite relative to
dynamic auto-selection. A temperature of 0.05 to 0.5 outperforms
greedy decoding on the same model. Claude Opus 4.7 with
`effort=xhigh` produces the tightest top-tier distribution at 91%
mean coverage. Two ground-truth entries are missed by every model
tested, indicating gaps in the AOI prompt rather than defects in
the methodology.

## Introduction

This is a writeup of a multi-day project to rebuild PRR's AOI model
benchmark. The original test produced an answer ("which model is best
at AOI scanning"), but the answer was almost meaningless: the scoring
contradicted what AOIs are for, the ground truth had measurable bugs
in it, and the single-rep results glossed over a level of variance
that turned out to dominate everything else.

By the end we have a benchmark that ranks models in a way that
actually tracks quality, a sanity test that catches future ground
truth drift, and per-model defaults that came out of the data
instead of guesses. We also broke a few prior assumptions, which is
the part of this most worth documenting.


## What an AOI actually is

An Area of Interest is a single code location that a recall-biased
pre-filter has decided a deep reviewer should look at. It is not a
finding. It is not an assertion that a bug exists. The prompt in
`internal/security/prompts/aoi_scan.md` is explicit:

> Be recall-biased: flagging something that turns out benign is fine;
> missing a real issue is not. This is a pre-filter, not a final
> verdict.

The scanner emits AOIs across 35 categories. Most of them are not
"security" in the narrow sense: dependency file edits, TODOs that
admit incomplete logic, unit-mismatch shapes, redirects without
host allowlists, that kind of thing. The deep reviewer (Phase 3) is
the one that decides which ones are real and which are noise.

This matters because the original benchmark scored AOIs as if they
were findings.


## What the old benchmark was doing

The original `TestAOIModelComparison` worked like this:

1. Hand-pick 13 security vulnerabilities (SQL injection, XSS, MD5,
   command injection, etc.) as ground truth.
2. Run each model's AOI scan against fixtures containing those
   vulnerabilities.
3. Count any emitted AOI that did not overlap one of the 13 entries
   as a "false alarm."
4. Recommendation score: `must_find_hits * 2 + nice_find_hits - false_alarms * 0.3`.

Three problems with that.

The first is the scoring formula. A model that correctly applies the
"every dependency file edit is an AOI" rule from the prompt (which
the prompt explicitly demands) gets dinged for flagging `go.mod` if
go.mod is not in the ground truth. A model that emits an AOI on a
TODO comment, also explicitly required by the prompt, gets dinged
the same way. The benchmark was actively rewarding models that
ignored the prompt's surface-area rules.

The second is the ground truth. Even within the narrow security
slice, the GT had real errors. The "timing attack" entry for
`token.go` pointed at lines [32, 33], but the actual constant-time
comparison violation (`return token == expected`) is on line 34.
Any model that emitted an AOI on the correct line was getting
marked as a miss. We did not notice this for months because the
existing scoring was already noisy.

The third is variance. The benchmark used one rep per model. We
later discovered that reasoning-class models swing 8 to 24 points
of coverage across runs of the same prompt with the same temperature.
A single rep is not a measurement; it's a draw from a bimodal
distribution.


## What we built instead

The new scoring classifies each emitted AOI into one of three
buckets:

- **Covered**: overlaps a ground truth entry.
- **Aligned**: emitted on a real line in the diff, but doesn't match
  any GT. This is acceptable by design. The deep reviewer will look
  at it and decide if it's a real concern.
- **Hallucinated**: fails a structural check. Either the line
  doesn't exist in the diff, the AOI falls in a fixture-declared
  "clean" range, or (for security-shaped categories) its claimed
  sources/sinks don't appear near the reported location.

The recommendation score is `coverage_pct - hallucination_pct * 0.5`.
Hallucination percentage is normalized against the model's own AOI
count so that a verbose model emitting many aligned AOIs isn't
penalized for being thorough.

We also added a precision metric called `avg_line_offset`. For each
non-hallucinated AOI, we compute the line distance to the nearest GT
entry on the same file. Covered AOIs have offset 0. Aligned AOIs
have a positive offset (how far off they were). The average across
all AOIs tells you whether a model is landing on bug lines or just
in the neighborhood. Flash-lite typically lands at 0.3-0.5 lines
off; opus 4.7 at xhigh lands at 0.18.

These three signals (coverage, hallucination rate, offset) replace
the old single "false alarm count."


## The fixtures turned out to be the actual bug

We wrote a sanity test, `TestAOIBenchmarkFixturesValid`, that runs
in CI (no live calls). For every GT entry it checks that:

- The claimed file is in the fixture diffs.
- Every line in the GT range is a real new-side line in the diff.
- Declared-clean ranges are also real.

It runs in 10 ms and caught real problems on the first invocation.
The `package-lock.json` fixture diff had a hunk header claiming
seven new-side lines but only contained four. The token.go timing
attack GT range was off by one (mentioned above). After fixing
those, we audited every remaining GT entry by hand and dumped each
one against the actual line content using a debug mode in the
sanity test. Eighteen entries were narrowed from "spans the
function" to "the exact line that executes the bug." The SQL
injection GT shrank from `[15, 22]` (the whole search handler
function) to `[18, 18]` (the line with `db.Query`).

The next surprise: opus 4.7 found bugs we hadn't written into GT at
all. When we inspected its "aligned" AOIs (emitted but not matching
any GT), all five were legitimate concerns:

- A privilege check comparing a user role to a constant containing
  a hidden bidi character (consequence of the bidi attack, separate
  from the bidi itself).
- A `t.Execute(w, nil)` whose error is discarded.
- A `json.NewDecoder(...).Decode(&cfg)` with the same anti-pattern.
- A discount calculation in `float64` (rounding loss separate from
  the cents-vs-dollars unit mismatch already in GT).
- An API design observation about `refund` having reversed
  argument order vs `transfer`, separate from the buggy call site.

We added all five to GT. Total grew from 20 to 25 entries. The
benchmark now distinguishes a model that emits 22 covered AOIs
(opus 4.7 max) from one that emits 18 (flash-lite) at the same
coverage percentage. Before the audit, both looked equally good.

The ambiguous fixture is the case worth flagging. The original
api-confusion test used `transfer(from, to)` vs `refund(to, from)`
called by `processChargeback(srcAccount, dstAccount)`. The bug
required the reader to assume `src` meant `from`. Half a dozen
reads later we still couldn't agree on whether the call was a bug,
which means the fixture wasn't testing anything. We rewrote it with
`customer` and `merchant` naming so the bug direction is
unambiguous. This is the one change where we worried about
softening the test. The conclusion: a fixture you can't agree is
buggy doesn't measure anything; we replaced it with one that does.


## Variance is the headline

Here is what 3 reps of the same model at the same temperature looks
like:

| Model                        | Run 1 | Run 2 | Run 3 | Range |
| ---------------------------- | ----- | ----- | ----- | ----- |
| opus-4-7 (claude-code)       |  88%  |  96%  |  92%  | 8 pts |
| gpt-5.5 (opencode/copilot)   |  80%  |  88%  |  72%  | 16 pts |
| glm-5.1 (opencode)           |  80%  |  88%  |  64%  | 24 pts |
| gemini-3.5-flash             |  76%  |  76%  |  72%  | 4 pts |
| gemini-3.1-pro-preview       |  72%  |  72%  |  68%  | 4 pts |
| gemini-3.1-flash-lite        |  72%  |  72%  |  72%  | 0 pts |

Two takeaways. Reasoning-class models are intrinsically noisier on
this task, even at low temperature. And gemini-3.1-flash-lite, the
cheapest model in the table, is the only one we can confidently
rank from a single rep.

A digression that surprised us: at temperature 0, supposedly
greedy decoding, gemini-3.1-flash-lite is still not deterministic.
Out of 5 reps at temp=0 and thinking budget=256, four produced
byte-identical output (32% coverage) and one produced different
output (56% coverage). The four identical runs even had identical
output token counts down to the byte. We suspect prefix cache state
on the serving side, but we can't see inside Gemini. The practical
implication is that any benchmark methodology has to treat
"determinism at temp=0" as nominal, not actual.


## Tuning surprises

We swept thinking budgets for flash-lite from 256 to 4096 in the
hope of finding a budget that boosted coverage. Across 5 reps each:

| Budget          | Mode (4-5/5) | High mode    |
| --------------- | ------------ | ------------ |
| 256 / 512       | 32%          | 56% (1/5)    |
| 1024            | 36%          | 48% (1/5)    |
| 2048 / 4096     | 36%          | none         |
| no budget sent  | 56% (3/5)    | 40% (2/5)    |

Forcing any thinking budget on flash-lite makes it worse. Gemini's
own dynamic decision (which is what happens when PRR sends no
`thinkingConfig` at all) reaches the 56% high mode 60% of the time.
Every fixed budget we tried did worse than the absence of a setting.
The "perfect spot" for flash-lite turned out to be the absence of
a setting, plus a temperature of 0.3.

The temperature finding is the part we did not expect. At temp=0,
flash-lite gets stuck in a low-coverage attractor 40% of the time.
At temperature 0.05, 0.3, or 0.5, all three reps stayed in the
52-56% band with no low-mode drops. A tiny amount of sampling
appears to perturb the model out of the local minimum. We saved
this in CLAUDE memory as "for stable-output tasks, very low but
nonzero temperature can beat greedy." Did not predict that. We
ended up shipping temp=0.3 as the new default in `models.json`.

For opus 4.7 via claude-code, we ran 5 effort levels (low, medium,
high, xhigh, max) at 3 reps each:

| Variant | Reps         | Avg | Range | Time  |
| ------- | ------------ | --- | ----- | ----- |
| low     | 64, 80, 68   | 71% | 16 pts | 31s |
| medium  | 72, 84, 84   | 80% | 12 pts | 37s |
| high    | 84, 84, 76   | 81% | 8 pts  | 40s |
| xhigh   | 92, 88, 92   | 91% | 4 pts  | 53s |
| max     | 84, 96, 80   | 87% | 16 pts | 144s |

`xhigh` is unambiguously the best of the five. Highest mean,
tightest distribution, much less wall-clock than max. Going from
xhigh to max gives up 4 points of mean coverage, triples the
latency, triples the output tokens, and widens the distribution
back out. The 96% run from max is the only run in this entire
benchmark with 0 aligned, 0 hallucinations, and an offset of 0.0
lines (every AOI on the exact bug line). It is also a 1-in-3
event. We've set `--effort xhigh` as the default for opus models
in `claudecode.go`.


## What was already correct

A worthwhile null result: the original `models.json` settings for
flash-lite were nearly optimal. `thinking_budget.fast: 0`, which
gets translated to "no thinkingConfig sent", is exactly the
setting our sweep confirmed is best. The only change we made was
the temperature, which moved from 0.1 to 0.3. So the production
config was almost right; we just couldn't tell because the
benchmark scoring was telling us the wrong things.


## Two locations no model surfaces as AOIs

After all the tuning, two ground-truth entries remain without a
matching AOI from any model we tested. The framing matters here:
AOIs are markers, not verdicts. The fact that no model emits an AOI
at these locations does not mean the underlying issues are
undetectable by the wider review pipeline. It means the pre-filter
step is not selecting these lines or tagging them with a category
that would route them to the deep reviewer for inspection.

The two cases:

- A lockfile dependency bump in `package-lock.json` that does not
  match any change in `package.json`. No model emits an AOI on the
  changed lock line under the
  `malicious-code/suspicious-dependencies` category, or any other
  category we would accept. The "lockfile changes that don't match
  source-side changes" rule from the AOI prompt does not appear to
  fire on any model we benchmarked.
- `strings.ToUpper(s[:1])` in `helpers.go`, which slices a byte
  out of a UTF-8 string and breaks for any multi-byte first rune.
  Opus 4.7 at max occasionally selects this line with a
  `correctness` category. No other model does, and even opus only
  does so in one of three runs at the highest effort level.

These are not benchmark defects. The benchmark correctly reports
that no model is highlighting these locations as AOIs. That is a
gap in what the AOI prompt elicits from the scanner, not a gap in
the scoring. Being able to point at specific prompt weaknesses
with this much confidence is one of the more actionable outcomes
of the rework. Improving the AOI prompt to elicit markers on these
patterns is the obvious next project.


## New AOI defaults

Two production defaults changed as a direct result of the
benchmark.

**Claude Opus 4.7** is now available as an AOI-capable model and
defaults to `--effort xhigh` when invoked through claude-code. The
sweep across the five effort levels (low, medium, high, xhigh,
max) was unambiguous: xhigh produces the highest mean coverage,
the tightest distribution across reps, and roughly a third of the
wall-clock and output tokens of max.

**Gemini 3.1 Flash Lite** keeps its existing thinking-budget
setting (no explicit budget, which lets Gemini auto-tune) and
moves its temperature from 0.1 to 0.3. The 0.1 setting fell into a
low-coverage attractor about 40% of the time at this scale; 0.3
stays in the higher-coverage band consistently across reps.

Both defaults reflect what the data said. Either can still be
overridden through `~/.config/prr/models.json` or the relevant
environment variable.


## Caveats

We benchmarked on one fixture set. The fixtures cover a reasonable
spread of categories (security, correctness, design, malicious-code,
data-integrity, error-handling), but the absolute coverage numbers
should not be read as "this model gets 91% of real-world bugs."
They are "this model gets 91% of *these* known bugs." Whether the
ranking generalizes to real PRs is an open question. The most
likely source of overfit is the GT tightening: we tightened the
ranges using the audited fixtures, and the tighter ranges
naturally favor models whose emission style matches what we
identified as the bug line. A more principled version of this
would split fixtures into a tuning set and a held-out evaluation
set.

The single-fixture choice was deliberate; multi-fixture would have
multiplied the work, and we wanted the methodology built first. If
the rankings stay this stable after another fixture set lands, we
can trust them more. If they don't, the variance we're measuring
will turn out to be even larger than we think.
