---
description: Implement one item from docs/plan
argument-hint: <item id, e.g. 0.1>
---
Implement plan item $ARGUMENTS.

1. Read docs/plan/INDEX.md, then only the workstream file that contains $ARGUMENTS. If an item in its Needs column is not done, stop and say what is missing.
2. Find the code with targeted searches in the paths under "Where". Don't explore the repository broadly or read files you won't change.
3. If the item is M or L, write a plan of 10 lines or fewer and wait for my OK. If it is S, go ahead.
4. Write tests for the "Done when" condition first, then the code. Run only the affected packages (go test ./internal/<pkg>/...). Run go vet ./... && go test ./... once at the end.
5. Don't run live probes, paid benchmarks or anything that needs an API key. If the item needs one, build it, add the manual steps to docs/, and mark the item blocked(<reason>).
6. Set the item's Status in INDEX.md, and add a CHANGELOG line. Add a PRIVACY.md entry if you added a table.
7. Reply with at most 5 lines: files changed, tests run and their result, anything left open. Don't restate the plan or paste code.
