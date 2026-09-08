-- The initial DevDuel schema.
--
-- Three invariants are enforced here rather than in Go, because they are the
-- ones that are expensive to discover late: challenges are immutable, the
-- match event log is append-only with a gapless per-match sequence, and a
-- player has at most one judge job in flight at a time.

-- reject_mutation is the enforcement behind "immutable" and "append-only".
-- The tables that use it are the ones where a later edit would silently
-- rewrite history that something else already read.
create function reject_mutation() returns trigger language plpgsql as $$
begin
    raise exception '% is not allowed on %', tg_op, tg_table_name
        using errcode = 'restrict_violation';
end;
$$;


-- Players ---------------------------------------------------------------

create table users (
    id            uuid primary key,
    email         text not null check (email <> '' and length(email) <= 320),
    username      text not null check (username <> '' and length(username) <= 32),
    -- Whatever the hashing scheme encodes, in its own self describing format.
    -- Nothing outside internal/auth may read this column.
    password_hash text not null check (password_hash <> ''),
    created_at    timestamptz not null default now()
);

-- Case insensitive uniqueness without the citext extension: a migration that
-- installs an extension needs privileges a migration should not need.
create unique index users_email_key on users (lower(email));
create unique index users_username_key on users (lower(username));

create table ratings (
    user_id    uuid primary key references users (id) on delete cascade,
    rating     integer not null,
    -- Rated games played. Provisional rating periods read this.
    games      integer not null default 0 check (games >= 0),
    updated_at timestamptz not null default now()
);


-- Challenges ------------------------------------------------------------

-- A challenge version is immutable, so an already played match can always be
-- explained by the exact spec it ran under. A change is a new version, never
-- an edit, which is what the reject_mutation triggers below enforce.
--
-- The challenge directory on disk stays the source of truth for the image,
-- the hidden tests, the starting workspace and the solution. These tables
-- hold the registration of a version plus the fields the API has to render
-- without reading the repository.
create table challenges (
    id                text not null check (id ~ '^[a-z0-9]+(-[a-z0-9]+)*$'),
    version           integer not null check (version > 0),
    category          text not null check (category in ('debugging', 'build')),
    difficulty        text not null check (difficulty in ('easy', 'medium', 'hard')),
    -- An interval rather than a number of seconds, so that the deadline of a
    -- match is computed as started_at + duration in one place, in SQL.
    duration          interval not null check (duration > interval '0'),
    image_tag         text not null check (image_tag <> ''),
    created_at        timestamptz not null default now(),
    primary key (id, version)
);

create table requirements (
    challenge_id      text not null,
    challenge_version integer not null,
    -- The key the tester reports results under.
    key               text not null check (key <> ''),
    -- Checklist order, taken from the spec. Requirements are not sorted by
    -- key: the author's order is part of how the challenge reads.
    position          integer not null check (position >= 0),
    title             text not null check (title <> ''),
    description       text not null,
    weight            integer not null check (weight > 0),
    -- Declared to fail against the starting workspace. devduelctl asserts it.
    broken            boolean not null default false,
    primary key (challenge_id, challenge_version, key),
    unique (challenge_id, challenge_version, position),
    foreign key (challenge_id, challenge_version)
        references challenges (id, version) on delete cascade
);

-- Immutable means no edits. Deleting a version that was never played is
-- allowed, and a version that was played is held down by the match foreign
-- key below rather than by a trigger.
create trigger challenges_immutable before update on challenges
    for each row execute function reject_mutation();
create trigger requirements_immutable before update on requirements
    for each row execute function reject_mutation();


-- Matches ---------------------------------------------------------------

-- event_seq is the allocator for match_events.seq. Bumping it and inserting
-- the event in one transaction is what makes the sequence gapless, and
-- gapless is what lets a reconnecting client tell "nothing happened" from
-- "an event was lost".
create table matches (
    id                uuid primary key,
    challenge_id      text not null,
    challenge_version integer not null,
    lobby_code        text not null check (lobby_code <> ''),
    state             text not null default 'lobby'
                      check (state in ('lobby', 'active', 'judging', 'complete', 'abandoned')),
    event_seq         bigint not null default 0 check (event_seq >= 0),
    created_at        timestamptz not null default now(),
    started_at        timestamptz,
    -- The clock. Server authoritative: clients render this, they never
    -- compute it, and remaining time is never stored anywhere.
    deadline_at       timestamptz,
    ended_at          timestamptz,
    -- Null for a draw as well as for a match that has not finished. Which of
    -- the two it is follows from state.
    winner_user_id    uuid,

    foreign key (challenge_id, challenge_version)
        references challenges (id, version) on delete restrict,

    -- The clock is set once, when the match starts.
    constraint matches_clock_is_whole check ((started_at is null) = (deadline_at is null)),
    -- A match that got as far as being played has a clock. A lobby that was
    -- abandoned before anyone started it never had one.
    constraint matches_started_once_played check (
        state in ('lobby', 'abandoned') or started_at is not null
    ),
    constraint matches_deadline_after_start check (deadline_at is null or deadline_at > started_at),
    constraint matches_ended_after_start check (ended_at is null or ended_at >= started_at)
);

-- A lobby code only has to be unique among matches somebody could still join,
-- so codes are recycled once a match starts.
create unique index matches_open_lobby_code_key on matches (lobby_code) where state = 'lobby';
create index matches_challenge_idx on matches (challenge_id, challenge_version);

create table match_players (
    match_id      uuid not null references matches (id) on delete cascade,
    user_id       uuid not null references users (id) on delete restrict,
    -- Which side of the board. Two players, so two slots.
    slot          smallint not null check (slot in (1, 2)),
    joined_at     timestamptz not null default now(),
    submitted_at  timestamptz,
    -- Fraction of the challenge's total requirement weight that passed.
    -- M6 owns how it is computed; the range is all this table asserts.
    score         numeric(5, 4) check (score >= 0 and score <= 1),
    rating_before integer,
    rating_after  integer,
    primary key (match_id, user_id),
    unique (match_id, slot)
);

-- Match history, keyed by player.
create index match_players_user_idx on match_players (user_id);

-- The winner has to be one of the two players. Stating that as a foreign key
-- means no code path can record a winner who was not in the match.
alter table matches add constraint matches_winner_played
    foreign key (id, winner_user_id) references match_players (match_id, user_id);


-- Workspaces ------------------------------------------------------------

-- This table is the workspace. There is no long lived container and no
-- per player volume: a judge run bind mounts a directory materialised from
-- these rows for the few seconds it takes, and then that directory is gone.
create table workspace_files (
    match_id   uuid not null,
    user_id    uuid not null,
    path       text not null,
    -- bytea rather than text because text cannot hold a NUL byte, and an
    -- upload path will eventually hand us one.
    content    bytea not null,
    updated_at timestamptz not null default now(),
    primary key (match_id, user_id, path),
    foreign key (match_id, user_id)
        references match_players (match_id, user_id) on delete cascade,

    -- A path is relative, normalised, and cannot climb out of the workspace.
    -- The store layer checks this too; the constraint is what makes the check
    -- unbypassable.
    constraint workspace_files_path_shape check (
        path <> ''
        and length(path) <= 512
        and path !~ '^/'
        and path !~ '/$'
        and path !~ '//'
        and path !~ '(^|/)\.\.?(/|$)'
    ),
    constraint workspace_files_size check (octet_length(content) <= 1048576)
);


-- Judging ---------------------------------------------------------------

create table judge_jobs (
    id           uuid primary key,
    match_id     uuid not null,
    user_id      uuid not null,
    state        text not null default 'queued'
                 check (state in ('queued', 'running', 'succeeded', 'failed')),
    -- Retries of the same logical run, so a job that failed on
    -- infrastructure can be told apart from one the player asked for again.
    attempt      integer not null default 1 check (attempt > 0),
    requested_at timestamptz not null default now(),
    started_at   timestamptz,
    finished_at  timestamptz,
    -- Why the job failed, and null on every other state. This is the judge
    -- failing, not the player's code failing: a workspace that fails every
    -- requirement is a succeeded job.
    error        text,

    foreign key (match_id, user_id)
        references match_players (match_id, user_id) on delete cascade,

    constraint judge_jobs_error_iff_failed check ((state = 'failed') = (error is not null)),
    constraint judge_jobs_finished_when_terminal check (
        (state in ('queued', 'running')) = (finished_at is null)
    ),
    constraint judge_jobs_started_before_finished check (
        finished_at is null or (started_at is not null and finished_at >= started_at)
    )
);

-- One judge run per player at a time. A player hammering the Run button
-- queues nothing new while their previous run is still going.
create unique index judge_jobs_one_in_flight on judge_jobs (match_id, user_id)
    where state in ('queued', 'running');

-- Covers the foreign key, and answers "this player's runs, newest first".
create index judge_jobs_player_idx on judge_jobs (match_id, user_id, requested_at desc);

-- One row per declared requirement per job. A tester that crashed still
-- produces a full set, with status 'error', because a missing row and a
-- failing row must never score the same.
create table judge_results (
    job_id          uuid not null references judge_jobs (id) on delete cascade,
    requirement_key text not null check (requirement_key <> ''),
    status          text not null check (status in ('pass', 'fail', 'error')),
    duration_ms     integer not null default 0 check (duration_ms >= 0),
    message         text not null default '',
    primary key (job_id, requirement_key)
);


-- Event log -------------------------------------------------------------

-- Append only, ordered by a per match sequence starting at 1. WebSocket
-- resume is "everything after seq N", which only works if an event's seq and
-- payload never change and no event is ever removed. The foreign key
-- restricts rather than cascades for that reason: a cascade would have to
-- delete rows the trigger refuses, so a match with history cannot be deleted
-- at all, which is the same statement said twice.
create table match_events (
    match_id   uuid not null references matches (id) on delete restrict,
    seq        bigint not null check (seq > 0),
    type       text not null check (type <> ''),
    payload    jsonb not null default '{}'::jsonb,
    created_at timestamptz not null default now(),
    primary key (match_id, seq)
);

create trigger match_events_append_only before update or delete on match_events
    for each row execute function reject_mutation();
