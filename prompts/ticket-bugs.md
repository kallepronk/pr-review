Dimension: bugs

Question: does this hunk introduce a correctness bug on the changed lines?

A correctness bug means: given some realistic input or state, the changed code produces a wrong result, crashes, leaks a resource, races, or silently drops data. Examples that count: off-by-one, wrong comparison operator, nil or null dereference on a reachable path, error swallowed and execution continues on bad state, wrong variable used, missing return after handling an error case, inverted condition, unchecked index or map access, wrong unit or type conversion.

You have read-only tools. Use them only to confirm a suspicion: read the surrounding function, or grep for callers when the bug depends on how the function is called. Do not browse.

Report at most three findings, the most serious first. Severity high means it will happen in normal use; medium means it needs an unusual but realistic input; low means edge case only.

File: {{.Path}}

Hunk (unified diff, only `+` lines and unchanged context are current code):

```
{{.Patch}}
```
