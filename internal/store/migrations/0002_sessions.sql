-- Sessions, the thing a login hands back.
--
-- The token itself is never stored. What is stored is its SHA-256, so a
-- database that leaks does not hand over a set of live logins. SHA-256 rather
-- than a password hash is deliberate: a session token is 256 bits from a
-- cryptographic source, so there is no guessable input to slow an attacker
-- down over, and making every authenticated request pay for a memory-hard
-- hash would be a denial of service with extra steps.
create table sessions (
    -- The digest is the identity: nothing else about a session is secret, and
    -- there is no second key to keep in step with it.
    token_hash bytea primary key check (octet_length(token_hash) = 32),
    user_id    uuid not null references users (id) on delete cascade,
    created_at timestamptz not null default now(),
    expires_at timestamptz not null,

    constraint sessions_expire_after_they_start check (expires_at > created_at)
);

-- Signing out everywhere, and the cascade above.
create index sessions_user_idx on sessions (user_id);

-- Expiry is enforced when a session is read rather than by a sweeper, so an
-- expired row is inert the moment it expires whether or not anything has got
-- around to deleting it. The index keeps a cleanup job cheap when there is one.
create index sessions_expires_at_idx on sessions (expires_at);
