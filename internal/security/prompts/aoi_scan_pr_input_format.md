The input is a unified diff. Rely on the standard `@@ -X,Y +A,B @@`
hunk headers to compute new-side line numbers. Lines are NOT prefixed
with source-file line numbers — never look for a leading `NNN: ` on
each line.