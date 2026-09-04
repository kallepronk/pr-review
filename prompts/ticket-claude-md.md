Dimension: claude-md

Question: does this hunk violate a rule that is literally written in the project instructions below?

Only a rule that is written down counts. Quote the exact sentence from the instructions in the `evidence` field together with the code that breaks it. If the instructions do not mention the topic, there is no finding. Instructions about how to write code sometimes do not apply to reviewing it (for example instructions about which commands to run); skip those.

Project instructions (CLAUDE.md / AGENTS.md files, concatenated):

```
{{.ClaudeMD}}
```

File: {{.Path}}

Hunk:

```
{{.Patch}}
```
