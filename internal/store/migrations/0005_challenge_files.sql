-- The starting workspace, stored beside the challenge version that defines it.
--
-- A match seeds both players' trees from these rows in the same transaction
-- that starts the clock. That makes starting a match a database operation
-- rather than a filesystem one: no instance needs the challenge directory on
-- disk to start a game, both players provably begin from identical bytes, and
-- a match played a year ago can still be explained from the database alone.
--
-- Immutable, like the challenge and its requirements. A change to the starting
-- workspace is a new challenge version, never an edit, which is what the
-- trigger below enforces and what registration checks by comparing digests.
create table challenge_files (
    challenge_id      text not null,
    challenge_version integer not null,
    -- Relative to the workspace root, and the same shape a player's file
    -- takes: these rows are copied into workspace_files verbatim, so a path
    -- that is legal here has to be legal there.
    path              text not null,
    content           bytea not null,
    primary key (challenge_id, challenge_version, path),
    foreign key (challenge_id, challenge_version)
        references challenges (id, version) on delete cascade,

    constraint challenge_files_path_shape check (
        path <> ''
        and length(path) <= 512
        and path !~ '^/'
        and path !~ '/$'
        and path !~ '//'
        and path !~ '(^|/)\.\.?(/|$)'
    ),
    constraint challenge_files_size check (octet_length(content) <= 1048576)
);

create trigger challenge_files_immutable before update on challenge_files
    for each row execute function reject_mutation();
