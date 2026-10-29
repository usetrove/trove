# Datasets: what we may use and publish

Checked 2026-10-29 against the primary sources linked below. This is not legal advice; re-check before publishing results.

| Source | Terms found | What bench/ does |
|---|---|---|
| SWE-bench (code) | MIT, copyright the SWE-bench authors ([LICENSE](https://github.com/SWE-bench/SWE-bench/blob/main/LICENSE)) | Uses the harness ideas and instance metadata. |
| SWE-bench Verified (data) | The [Hugging Face dataset card](https://huggingface.co/datasets/princeton-nlp/SWE-bench_Verified) shows **no license tag**. The tasks are built from 12 upstream Python repos whose own licenses apply. | Downloads at run time, pins the dataset revision, never redistributes issue text or patches. Publishes only task IDs, our metrics and links. |
| Aider polyglot benchmark | "All exercise content is copyright © Exercism", used under Exercism's open-source licenses; the repo has no license file of its own ([repo](https://github.com/Aider-AI/polyglot-benchmark)). | Clones at a pinned commit for local use only. Does not commit or republish exercise text. Keeps the attribution in each manifest. |
| Terminal-Bench | Apache-2.0 for the repo ([repo](https://github.com/laude-institute/terminal-bench)). The README states no terms for publishing results. | Not used in v1. Check the leaderboard guide at tbench.ai before adding it or publishing numbers. |
| Mined tasks | Each task is an issue and PR from a repo with a permissive license (MIT, Apache-2.0, BSD). | Records repo, commit, issue link, PR link and license in the manifest. Mined manifests stay local (gitignored). |

Publishing rule of thumb: our own aggregate numbers, task IDs and links are fine. Do not publish task prompts, upstream code or patches from sources whose terms are unclear.
