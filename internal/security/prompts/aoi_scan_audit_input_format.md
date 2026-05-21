Every input line is prefixed with its source line number followed by
`: `, like ` 42: <line content>` (the number is left-padded with
spaces so the column of `:` lines up). The number is the source line
number of the original file.

When you emit `line` and `end_line` for AOIs, copy the exact number
you see at the start of the line — do not compute, derive, count, or
estimate. The prefix is the only source of truth for line numbers in
audit output. If your AOI spans multiple lines, set `end_line` to the
prefix number on the last line of the span.