1. ONLY flag code in the DIFF (added or modified lines, the + lines).
2. Do NOT flag pre-existing code that was not changed.
3. Use the CONTEXT lines (unchanged lines around the diff hunks) to understand
   data flow — trace where variables originate and how they reach sinks.
   The diff may include extra context lines beyond the standard 3 to help you
   see the full picture. Use them.