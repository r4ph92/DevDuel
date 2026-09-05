# DevDuel

Competitive software engineering for modern developers. Two developers get the
same real engineering task, the same starting repository and the same clock,
and the better working solution wins.

The premise is that traditional competitive programming tests the one thing
that stopped being scarce: producing code. DevDuel tests what is still hard —
understanding requirements, working inside unfamiliar code, debugging, and
shipping something that actually holds up. **AI assistance is allowed by
design**, so challenges are chosen to be ones a model cannot simply one-shot.

> **Status:** pre-implementation. Architecture is settled, nothing is built yet.

## How a match works

```
queue → match created → isolated workspace → coding → judge → live score → winner → rating
```

Both players get the same broken (or empty) repository and a checklist of named
requirements. As they work they can run the judge, which reports which
requirements now pass. They never see the tests.

## Architecture decisions

Two decisions shape everything else.

**The workspace is a database table, not a container.** Players get an editor
and fixed Run/Test actions rather than a shell, which means no long-lived
container has to exist per player. Files live in `workspace_files`, and a
container is created only for the few seconds a judge run takes. Players cannot
reach each other's workspace because there is no persistent process to reach.

**All judging is black-box over HTTP.** Hidden tests never run in the same
process — or the same container — as player code.

```
runner container  ←── HTTP ──  tester container
(player's code)                (hidden tests)
   both on one throwaway internal network, no egress
```

This costs a readiness-polling loop at startup and buys three things: challenges
are not welded to one language, tests are physically unreachable by the player,
and Debugging Mode and Build Mode become the same judge — only the contents of
the starting repository differ.

Because the judge network has no egress, **challenge images must ship with
dependencies pre-baked**. Installing packages at judge time cannot work, by
design.

## Planned layout

```
cmd/api/           HTTP + WebSocket. Auth, workspace CRUD, match state.
cmd/judge/         Queue consumer. The only component with Docker socket access.
cmd/devduelctl/    Challenge authoring and verification CLI.
internal/          match, matchmaker, workspace, judge, rating, realtime, store
challenges/        One directory per challenge: spec, image, workspace, tests, solution
web/               Vite + React + Monaco
```

Postgres holds users, ratings, challenges, matches, workspace files and an
append-only `match_events` log (WebSocket reconnect is "everything after seq N").
Redis holds the job queue, pub/sub fan-out, rate limits and the matchmaking set.

## Build order

The riskiest component comes first and needs no web code at all.

1. `devduelctl challenge verify` — one challenge, the judge package, driven from
   the CLI. A challenge is only valid if the broken repo fails exactly the
   declared requirements, the reference solution passes all of them, and both
   results reproduce.
2. Schema, auth, workspace CRUD
3. Match lifecycle behind a lobby code (no matchmaking yet)
4. Event log and WebSocket reconnect
5. Monaco, file tree, requirement checklist, server-authoritative timer
6. Elo, result screen, match history
7. Matchmaking queue

## Development

```bash
cp .env.example .env
docker compose up --wait   # postgres + redis; blocks until both are healthy
go test ./...
```

`--wait` matters: with plain `-d` the services are merely started, not ready,
and tests can connect before either accepts connections.

Requires Docker running locally — the judge talks to the Docker daemon, and
Postgres and Redis run as containers.

### Resetting the database

`POSTGRES_USER`, `POSTGRES_PASSWORD` and `POSTGRES_DB` are read by the Postgres
image only on first initialization, while the `postgres-data` volume is empty.
Changing them in `.env` afterwards has no effect on a database that already
exists.

To make new credentials take, the volume has to go — **this destroys every
local match, user and challenge row**:

```bash
docker compose down -v     # destructive: deletes postgres-data and redis-data
docker compose up --wait
```

## Contributing

`main` is protected. Work on a branch, open a pull request, let CI and
CodeRabbit review it, then merge.
