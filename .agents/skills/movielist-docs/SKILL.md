---
name: movielist-docs
description: Update MovieList documentation after implemented changes, or document project skills and workflows. Use for README, CHANGELOG and project-instruction synchronization; implementation, log analysis and builds are separate tasks.
---

# MovieList Documentation

Update the requested documentation in this repository. Follow [AGENTS.md](../../../AGENTS.md), preserve existing user edits, and keep user-facing documentation in Ukrainian unless the target file uses another language.

## Establish scope and evidence

Inspect git status and relevant diffs. If the implementation was already committed, inspect the relevant recent commits or files rather than assuming a clean working tree means nothing changed. Use the current conversation to identify the requested changes; do not rewrite unrelated documentation or historical handoff notes.

Read only the code, skills and verification results needed to substantiate the update. Describe implemented behavior, defaults, limitations and actual commands. Separate proposed behavior from implemented behavior, and automated checks from manual production evidence. Do not claim checks passed without evidence or mark production/checklist items complete based on a build or synthetic test.

## Update the right source

- README.md: user/developer instructions, configuration, current workflows and project-skill descriptions with links and concise invocation examples.
- CHANGELOG.md: notable delivered changes and observed verification, in a dated entry when warranted. Preserve historical entries and release numbers; routine wording edits need no changelog entry.
- AGENTS.md: maintained architecture contracts and agent workflow rules affected by the change. Do not turn an observation or proposal into a new critical invariant.
- .env.example: documented configuration keys/defaults when the implemented configuration changes. Never read or reproduce credentials from a real .env just to update documentation.
- Project SKILL.md files: descriptions and workflow guidance when explicitly requested or needed to keep the documented skill contract consistent. Preserve their execution and authorization boundaries.

internal/version/VERSION is the only editable release source. Documentation updates do not imply a release bump, regeneration of metadata, application build or deployment. Keep the README skill catalog limited to repository workflows; do not copy the machine's entire installed-skill catalog.

## Verify and deliver

Check relative links, paths, command examples, UTF-8 without BOM, LF endings and git diff --check. Validate new or changed skills with the available skill-creator quick_validate.py. No application tests or build are required for documentation-only changes.

Report which documents changed, any unresolved factual gaps, and completed documentation checks. Updating the docs must not silently change application code, catalog data, runtime config or checklist status.
