# Contributing to Mousa

Mousa is pre-alpha. Before proposing a change, check the supported behavior in [the capability matrix](docs/CAPABILITIES.md) and search existing issues and pull requests. Keep one bounded change or a coherent set of related fixes per pull request. Use a Conventional Commit-style PR title such as `fix(docs): correct source attribution`.

The maintainer commits directly to `main` without issues or pull requests. The sections below apply to outside contributions.

## Pull requests

Use the [pull request template](.github/PULL_REQUEST_TEMPLATE.md) for web-created and API-created PRs. Describe what changed, why, and actual proof that the changed behavior works. Record exact checks and results, including failures or checks not run. Select only testing boxes that apply; they are not an ordered or all-required list. Before requesting review, the author must check all four author-review attestations after reviewing the final diff, behavior, rationale, and evidence. Checking a box is not a substitute for the described evidence or an independent review. No automated rejection or approval is installed by this template.

If a PR fully resolves an issue, add `Closes #123` to its description. Repeat the full keyword for each issue, for example `Closes #12` and `Closes #18`. Reference an issue without a closing keyword if the PR only makes partial progress. GitHub closes linked issues when the PR is merged into the default branch, not when it opens. Avoid including private data, credentials, or unverified claims in public text.

## Issues

Use the [maintenance issue template](.github/ISSUE_TEMPLATE/maintenance.md) for a bounded defect or improvement. Include observed versus expected behavior or a concrete caller need, its impact, available evidence, and an observable completion condition. Select only the applicable triage boxes. A small nonblocking fix may be filed separately so current feature work can finish; do not defer a failing required check, security concern, or broken supported behavior as routine cleanup. Check for an existing issue before filing. Public issues are not a channel for confidential reports.

Templates guide authors; they do not enforce requirements automatically. Maintainers assess relevance, validation, compatibility, and security before merging. No issue is required for a PR when the change and evidence are already clear.
