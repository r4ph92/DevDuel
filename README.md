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
internal/          api, auth, match, matchmaker, workspace, judge, rating,
                   realtime, store
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
go run ./cmd/devduelctl db migrate
go test ./...
```

`--wait` matters: with plain `-d` the services are merely started, not ready,
and tests can connect before either accepts connections.

Requires Docker running locally: the judge talks to the Docker daemon, and
Postgres and Redis run as containers. If something already listens on 5432,
change `POSTGRES_PORT` in `.env` and change the port in `DATABASE_URL` to
match; they are two separate settings.

### Migrations

The schema lives in `internal/store/migrations`, embedded in the binary and
applied by `devduelctl db migrate`. Migrations go forward only. There are no
down migrations, and an already applied migration is never edited: the
migrator checksums what it ran and refuses to continue against a database
whose history disagrees with the files. Undoing a schema change is a new
migration, written deliberately.

Migrating is an operator step rather than something a server does to itself on
boot, since several API instances starting at once would all attempt it and a
schema change deserves someone watching.

### Running the API

```bash
go run ./cmd/devduelctl db migrate
go run ./cmd/api
```

`API_ADDR`, `DATABASE_URL` and `COOKIE_SECURE` are the whole configuration.
The server does not migrate on boot, since several instances starting at once
would all attempt it.

Four routes exist so far, plus `GET /health`:

```
POST /auth/register   {email, username, password} -> 201 {user}
POST /auth/login      {email, password}           -> 200 {user, expires_at} + session cookie
POST /auth/logout                                 -> 204
GET  /me              session cookie              -> 200 {user}
```

`GET /me` returns the signed-in user's ID, username, email and creation time.
Missing, malformed, expired and revoked sessions all return `401` with code
`unauthenticated`; database failures return `500`. Responses are marked
`Cache-Control: no-store`. Requests neither renew sessions nor change cookies,
including on authentication failure. Logout still clears the cookie.

Protected routes use authentication middleware and read the account from the
request context. Match and workspace routes must add resource authorization
when they are introduced: a valid session alone does not grant access to
another player's resources. Ratings and match history belong to the later
profile endpoint.

Passwords are argon2id, 64 MiB over two passes, with the parameters stored in
the hash so raising them later leaves existing hashes verifiable; a login
upgrades a hash that was made more cheaply. A session is 256 random bits in an
HttpOnly cookie, and the database stores only its SHA-256, so a database that
leaks does not hand over a set of live logins.

A failed login answers the same way whether the address is unknown or the
password is wrong, and takes the same time: when no account matches, the
password is still verified against a decoy hash. Registration answers the same
way whether it was the email or the username that collided. Neither of those
is free to give up later without turning a form into a way of asking who has
an account.

**Login is not rate limited yet.** Each attempt costs 64 MiB and about a tenth
of a second by design, which is a denial of service waiting for whoever finds
it first. That belongs with the rest of the abuse work in M8, and this API
should not be exposed to the internet before it lands.

### Database tests

`internal/store/storetest` gives each test its own database, cloned from a
template that is migrated once. Tests skip themselves when `DATABASE_URL` is
unset, so `go test ./...` still works before the compose services are up. CI
sets it against a service container, which is where the schema tests actually
have to run.

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
