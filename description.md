# Routing: load without reading every task record, and counting scopes and bursts

Follow-up from the #59 review (smaller points). `byLoad` (versed/route.go) has three problems:
- **Cost:** on every spawn with two or more fitting runners, it reads every record under the task instances. Nothing prunes those records, so the read grows with the whole task history. That is the reads × index-walk shape of staging's logd CPU. It applies wherever the local runner and a remote one offer the same doer.
- **Scopes:** it counts baseline only. A runner carrying 50 scoped tasks counts as 0.
- **Bursts:** load is read from records, which are written after the launch returns. Spawns firing together all see the same counts and all go to the first runner by name.

Counting live tasks in memory, from what verse hosts, would answer all three.