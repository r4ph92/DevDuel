# DevDuel — working agreements

## Commits

`prefix: imperative summary` — lowercase after the colon, no trailing period,
aim 50 characters, hard cap 72. Prefixes: `feat`, `fix`, `refactor`, `perf`,
`test`, `docs`, `style`, `build`, `ci`, `chore`, `revert`.

**No trailers.** No `Co-Authored-By`, no `Generated with`, no tool attribution
of any kind. The message ends with its own content.

One purpose per commit. If the subject line needs an "and", it is two commits.

Full convention: `~/.claude/skills/commit/SKILL.md`, or `/commit`.

## Branching

`main` is protected — pull request required, the `ci` check must pass, the
branch must be up to date, and every review thread must be resolved before
merge. Merges are squash-only and the branch is deleted after. Never commit to
`main` directly and never force-push it.

## Skill library

A personal skill library lives at `~/Documents/agentSkills`, symlinked into
`~/.claude/skills`. It comes in two kinds, and the distinction matters:

**Auto-invocable but path-scoped** — `backend`, `frontend`, `tdd`,
`api-contract`, `data-modeling`, `deps`, `observability`, `accessibility`,
`skill-authoring`. Each declares a `paths` glob and only offers itself when the
working tree holds matching files, so they appear as this repo grows. If one
seems missing, that is the scoping working, not a broken install.
`diagnosing-bugs`, `refactor` and `ui-design` are unscoped and always present.

**Slash-only** (`disable-model-invocation: true`) — `/planning`, `/grilling`,
`/commit`, `/peer-review`, `/qa-backend`, `/qa-frontend`, `/security-audit`,
`/performance`, `/release`. Their descriptions never enter context by design, so
they cannot fire on their own. When a task clearly calls for one and it has not
been invoked, read `~/.claude/skills/<name>/SKILL.md` directly and follow it.

Subagents `qa-runner` and `security-auditor` are installed and available.

## Architecture invariants

Decisions the code must not quietly break. Rationale is in the README.

- The **workspace is a database table**, not a long-lived container. A container
  exists only for the seconds a judge run takes.
- **Judging is black-box over HTTP.** Hidden tests never share a process or a
  container with player code, and the judge network has no egress — so challenge
  images must ship with their dependencies already baked in.
- The **match timer is server-authoritative**. Clients display it; they never
  compute it.
- Match state transitions are **idempotent**. Both players submitting and the
  deadline firing will race, and both paths must be safe.
