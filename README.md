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

`API_ADDR`, `DATABASE_URL`, `CHALLENGES_DIR` and `COOKIE_SECURE` are the whole
configuration. The server does not migrate on boot, since several instances
starting at once would all attempt it. It does register the challenge catalog
at boot, because registering is idempotent: the first instance to arrive
writes, and the rest check that what is there matches what they carry. A
challenge version that has changed without its version being bumped stops the
process rather than the match that would have played it.

These routes exist so far, plus `GET /health`:

```
POST /auth/register   {email, username, password} -> 201 {user}
POST /auth/login      {email, password}           -> 200 {user, expires_at} + session cookie
POST /auth/logout                                 -> 204
GET  /me              session cookie              -> 200 {user}

POST /matches                                     -> 201 {match}
POST /matches/join    {code}                      -> 200 {match}
GET  /matches/current                             -> 200 {match}
GET  /matches/{id}                                -> 200 {match}
POST /matches/{id}/leave                          -> 204
POST /matches/{id}/ready                          -> 200 {match}
POST /matches/{id}/submit                         -> 200 {match}
```

`GET /me` returns the signed-in user's ID, username, email and creation time.
Missing, malformed, expired and revoked sessions all return `401` with code
`unauthenticated`; database failures return `500`. Responses are marked
`Cache-Control: no-store`. Requests neither renew sessions nor change cookies,
including on authentication failure. Logout still clears the cookie.

Protected routes use authentication middleware and read the account from the
request context. Match routes add resource authorization on top of that: a
valid session says who somebody is, never what they may open, so every one of
them checks that the caller is a player in the match it names. A match
somebody is not in answers exactly like a match that does not exist, since a
403 would confirm that an id is real. Workspace routes will do the same.
Ratings and match history belong to the later profile endpoint.

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

### Lobbies

A lobby is a match that has not started: two seats and a join code, created by
one player and shared with the other however they like. The code is eight
characters from an alphabet with no `I`, `O`, `0` or `1`, and it is unique
only among lobbies that are still open, so codes are recycled once a match
starts. A response drops the code from that moment for the same reason.

A player is in at most one unfinished match at a time. That is a unique index
in the database rather than a check in Go, because creating and joining race:
`0003_lobby_membership.sql` keeps a derived flag on each seat and a partial
unique index over it, and the trigger that maintains the flag takes the match
row, which is what serialises two people pasting the same code at once.

A lobby does not say which challenge it is for. Waiting would otherwise be a
way to read the problem early, which is the point of a match having a start.
Leaving cancels the whole lobby and releases both players; leaving twice is
not an error, because a retried request should not become one. Starting the
clock belongs to the match state machine, and the lobby screens belong to the
web client.

### Match lifecycle

```
lobby --both ready--> active --both submitted--> judging
                         `------deadline-------'
```

A full lobby does not start on its own. Each player readies, and the second
ready starts the clock, so nobody loses minutes to an opponent who filled the
seat and walked away. `started_at` and `deadline_at` are written together from
the challenge's own duration, in SQL, so no caller anywhere decides how long a
match lasts, and remaining time is stored nowhere: a response carries the
deadline and the server's own `server_now` to measure it against.

Every transition is a compare and swap, an update guarded on the state it is
leaving, and the rows it touched say whether this caller was the one that
moved the match. Both players submitting and the deadline firing happen at the
same instant by design, so the loser of that race has to be a no-op rather
than a second transition. There is a test that fires all three at once and
asserts exactly one of them moved the match.

Each of those transactions takes the match row before it writes a player row,
because the trigger from 0003 updates every seat when a match changes state. A
writer that took a seat first and then reached for the match would deadlock
against one going the other way.

`judging` is where this stops for now. Reaching `complete` needs judge results
and scoring, which are their own issues, and the ticker that fires expiries
belongs to the timer work; this half only provides the transition it calls.

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

CodeRabbit is configured in `.coderabbit.yaml`, but **it does not review
automatically here**: its free tier for open source skips repositories with
fewer than 10 stars, and this one has one. Comment `@coderabbitai review` on a
pull request to get a review, until that changes.

`@coderabbitai review` is incremental: it looks only at commits added since the
last review. Ask for `@coderabbitai full review` when the whole pull request
should be read again from scratch, which is also the right command when a
review ran earlier and more commits have landed since.

It also skips a pull request whose base is not `main`, so a branch stacked on
another branch goes unreviewed even once the star rule is met. Prefer basing
work on `main`, and when a change really does have to stack, ask for the
review by hand.
