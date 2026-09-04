Dimension: security

Question: does this hunk weaken a trust boundary?

A trust boundary is weakened when data from outside the process (HTTP request, CLI argument, file, database row, environment, another service) reaches a sensitive sink without the check that the surrounding code applies elsewhere. Sensitive sinks: SQL or query builders, shell commands, file paths, HTML output, redirects, deserialisation, authorization decisions, logging of secrets, cryptographic choices.

Also report: a secret, token, or password written into code; an authorization check removed or bypassed; error messages that now leak internal details to a caller.

Use the read-only tools only to confirm where the input comes from or how the sink is used. If you cannot establish that the data is external, do not report it.

Report at most three findings.

File: {{.Path}}

Hunk:

```
{{.Patch}}
```
