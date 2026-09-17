<context_gathering>
Goal: Get enough context fast. Parallelize discovery. Stop when actionable.

Method:
- Start broad -> fan out to focused subqueries.
- Parallelize varied queries; read top hits. Deduplicate paths & cache; don't repeat.
- Avoid over-searching. Run targeted batch if needed.

Stop Criteria:
- Can name exact content to change.
- Top hits converge (~70%) on one area.

Escalation:
- If signals conflict/fuzzy: Run one refined parallel batch, then proceed.

Depth:
- Trace only modified symbols or direct contracts. Avoid transitive expansion.

Loop:
- Batch search -> Minimal plan -> Complete task.
- Re-search only if validation fails or new unknowns. Action > Search.
</context_gathering>

<self_reflection>
- First, define a rubric (5-7 categories) for a "world-class one-shot web app".
- Iterate internally until the solution hits top marks in all categories.
- Critical: Do not show the rubric to the user.
</self_reflection>

<persistence>
- Continue until the query is FULLY resolved. Do not yield early.
- On uncertainty: Research or deduce the most reasonable approach. Never stop.
- Do not ask for confirmation on assumptions. Decide, proceed, and document for reference later.
</persistence>

<code_editing_rules>
<principles>
- Readability: No non-standard chars/emojis in code/comments.
- Maintainability: Follow directory structure & naming conventions.
- Consistency: Adhere to design system (tokens, typography, spacing, components).
- Visuals: High quality (spacing, hover states) per OSS guidelines.
</principles>

<stack_defaults>
- Framework: Go Programming Language
</stack_defaults>
</code_editing_rules>

<project_details>
<instruction>
CRITICAL: You MUST read [README.md](README.md) BEFORE taking any action.
</instruction>
<development_rules>
- Developer documentation (except README.md) must be placed in the `Documents` directory.
- After every change, always run the linter and apply appropriate fixes. If intentionally allowing a linter error, document the reason in a comment. **Builds are for releases only and are not needed for debugging; running the linter is sufficient.**
- When implementing models, place one file per table.
- Reusable components must be implemented as separate files in the `modules` directory.
- Temporary scripts (e.g., investigation scripts) must be placed in the `scripts` directory.
- When creating or modifying models, update `Documents/テーブル定義.md`. Table definitions must be expressed as a table per database table, showing column names, types, and relations within the table.
- When system behavior changes, update `Documents/システム設計書.md`.
- When making notable changes, update `CHANGELOG.md` following the [Keep a Changelog](https://keepachangelog.com/) format. Entries must be written in English. `CHANGELOG.md` is for end users: describe each change concisely from the user's perspective (what they can now do or what visibly changes), not the implementation. Do NOT include internal details such as file paths, IPC channel/function names, internal data structures, or code-level mechanics. Record those developer-facing details in `Documents/システム設計書.md` instead.
</development_rules>
</project_details>
