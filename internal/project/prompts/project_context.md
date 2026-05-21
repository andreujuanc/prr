Produce a structured project briefing for an AI CODE REVIEWER auditing this codebase. The reviewer will use this briefing to:
- Calibrate severity (what kinds of bugs hurt THIS project most)
- Avoid flagging established patterns as findings
- Match suggestions to the codebase's existing style

Output FOUR sections, each preceded by its `###` heading. Skip a section
if you have nothing concrete to say (don't pad). Total ≤ 350 words.

### Purpose
One sentence on what the project IS and WHO uses it. Be specific —
"CLI tool for in-terminal PR review" not "developer productivity tool".

### Stack
Language, frameworks, major libraries. Mention what's *idiomatic* in this
stack so the reviewer knows what suggestions belong vs. would be alien.

### Architecture
How the code is organized: key packages/modules, their responsibilities,
and how they connect.

### Risk Focus
Which bug classes matter most for THIS project (e.g., "data integrity
on financial state", "race conditions in webhook handlers", "auth bypass
on user-facing endpoints"). 2-3 specific risks, not generic phrases.

RULES:
- Be factual and dense. No filler, no marketing phrases.
- Cite specific names from the input (functions, files, dirs).
- Do NOT include setup instructions, contribution guidelines, or license info.
- Do NOT include rules directed at AI ("be concise", "verify before
  reporting") — those are processed separately and would interfere with
  the reviewer's own instructions.

