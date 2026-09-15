#!/usr/bin/env python3
"""Convert a live LeapMux SQLite database's enum columns from words to proto ordinals.

The shipped migration is edited IN PLACE (this project ships no upgrade path
before release), so goose sees version 1 already applied and does nothing for a
database that predates the change. This script is that one-off conversion.

It brings the live schema up to the migration this build carries. It rebuilds
every table whose DDL differs from the migration, converts the enum words on the
way through, and replays every index and trigger the migration declares. SQLite
cannot alter a CHECK constraint, so a rebuild is the only route. The whole run
is ONE transaction with foreign keys off, and it ends with an integrity check, a
foreign-key check, and a comparison of the resulting schema against a database
built from the current migration -- so a rebuild that dropped an index or
mistyped a column fails here rather than at the next boot.

The repair and that final comparison read ONE source: the reference schema. A
hand-written list of tables to rebuild drifts away from the comparison as soon
as a change touches a table with no enum column, and the run then fails at the
gate with nothing to repair it.

One rebuild DROPS rows. Where the current schema adds a NOT NULL column with no
default, no old row states that column, so the table starts empty. The dry run
reports each such table before anything commits.

Usage:  migrate-enum-columns.py <hub.db|worker.db> [--apply]
Without --apply it reports what it would change and writes nothing.
"""
import argparse
import re
import sqlite3
import sys
from pathlib import Path
from typing import Any, NamedTuple

REPO = Path(__file__).resolve().parents[2]
HUB_MIGRATION = REPO / "backend/internal/hub/store/sqlite/db/migrations/00001_initial.sql"
WORKER_MIGRATION = REPO / "backend/internal/worker/db/migrations/00001_initial.sql"

# table -> {column: {stored word: proto ordinal}}. This map holds the VALUE
# conversion alone. It does not decide which tables the run rebuilds -- the
# schema diff does that -- so a table this change touched for another reason is
# rebuilt too. A word absent from a map is an unknown value: the run stops
# rather than guessing an ordinal for it.
HUB_COLUMNS = {
    "revocation_events": {"kind": {
        "session": 1, "session_revoked": 2, "api_token": 3, "api_token_rotation": 4,
        "delegation_token": 5, "user_tokens": 6, "user_info": 7}},
    "oauth_clients": {"registration_source": {
        "builtin": 1, "admin": 2, "user": 3, "dynamic": 4}},
    "oauth_states": {"purpose": {"login": 1, "reauth": 2}},
    "webauthn_sessions": {"kind": {
        "signup": 1, "login": 2, "register": 3, "elevation": 4, "recovery": 5}},
    "oauth_providers": {"provider_type": {"oidc": 1, "github": 2}},
    "lifecycle_outbox": {"op_type": {"create": 1, "rename": 2, "delete": 3}},
}
WORKER_COLUMNS = {
    # '' is "no goal", which is AGENT_GOAL_STATUS_UNSPECIFIED and the one
    # ordinal 0 this conversion writes.
    "agents": {"goal_status": {
        "": 0, "active": 1, "paused": 2, "blocked": 3, "done": 4}},
    "agent_todos": {"status": {
        "pending": 1, "in_progress": 2, "completed": 3, "deleted": 4}},
    "agent_background_tasks": {
        "kind": {"subagent": 1, "shell": 2},
        "status": {"pending": 1, "running": 2, "completed": 3, "failed": 4,
                   "stopped": 5, "interrupted": 6}},
    "control_response_answers": {"state": {
        "pending": 2, "uncertain": 3, "delivered": 4, "completed": 5}},
}


class Column(NamedTuple):
    """What one reference column requires of a row the rebuild writes."""

    not_null: bool
    default: str | None


class Reference(NamedTuple):
    """The schema this build expects, read from a database the migration built."""

    ddl: dict[str, str]         # object name -> its VERBATIM CREATE statement
    normalized: dict[str, str]  # object name -> its normalized DDL, for the comparison
    kind: dict[str, str]        # object name -> "table", "index" or "trigger"
    tables: list[str]           # every table name
    dependents: list[str]       # every index and trigger name
    columns: dict[str, dict[str, Column]]  # table name -> its columns


def goose_up(path: Path) -> str:
    """The migration's Up section, which is the schema this build expects."""
    text = path.read_text()
    up = text.split("-- +goose Up", 1)[1].split("-- +goose Down", 1)[0]
    # The worker migration wraps its triggers in StatementBegin/End markers,
    # which are goose directives rather than SQL.
    return re.sub(r"^-- \+goose Statement(Begin|End)\s*$", "", up, flags=re.M)


def normalize(sql: str) -> str:
    """DDL with comments and whitespace removed, so only the statement compares."""
    sql = re.sub(r"--[^\n]*", "", sql)
    return re.sub(r"\s+", " ", sql).strip()


def quote(identifier: str) -> str:
    """One identifier in SQLite's double-quoted form."""
    return '"' + identifier.replace('"', '""') + '"'


def reference_schema(migration: Path) -> Reference:
    """Build a database from the migration and read its schema back.

    The rebuild replays these statements rather than the ones the live database
    holds: an index whose predicate this change rewrote
    (idx_revocation_events_session_revoked now seeks `kind = 2`) would otherwise
    come back in its old form, and a partial index that no query matches is a
    silent full scan.
    """
    ref = sqlite3.connect(":memory:")
    ref.executescript(goose_up(migration))
    rows = ref.execute(
        "SELECT name, type, sql FROM sqlite_master WHERE sql IS NOT NULL").fetchall()
    tables = [name for name, kind, _ in rows if kind == "table"]
    # PRAGMA table_info gives (cid, name, type, notnull, dflt_value, pk).
    columns = {
        table: {row[1]: Column(bool(row[3]), row[4])
                for row in ref.execute(f"PRAGMA table_info({quote(table)})")}
        for table in tables
    }
    ref.close()
    return Reference(
        ddl={name: sql for name, _, sql in rows},
        normalized={name: normalize(sql) for name, _, sql in rows},
        kind={name: kind for name, kind, _ in rows},
        tables=tables,
        dependents=[name for name, kind, _ in rows if kind in ("index", "trigger")],
        columns=columns,
    )


def live_ddl(conn: sqlite3.Connection, name: str) -> str | None:
    """The CREATE statement the live database holds for one object, or None."""
    row = conn.execute("SELECT sql FROM sqlite_master WHERE name = ?", (name,)).fetchone()
    return row[0] if row else None


def refuse_unknown_values(conn: sqlite3.Connection, table: str, column: str,
                          mapping: dict[str, int]) -> None:
    """Stop rather than invent an ordinal for a word no mapping covers."""
    placeholders = ",".join("?" * len(mapping))
    unknown = conn.execute(
        f"SELECT DISTINCT {quote(column)} FROM {quote(table)} "
        f"WHERE {quote(column)} NOT IN ({placeholders})",
        tuple(mapping)).fetchall()
    if unknown:
        raise SystemExit(
            f"{table}.{column} holds values no mapping covers: {[u[0] for u in unknown]}")


def select_list(carry: list[str], convert_columns: dict[str, dict[str, int]]) -> tuple[str, list[Any]]:
    """The SELECT list that carries the old rows over, and its parameters.

    Each word binds as a PARAMETER. A literal needs SQL quoting, and two shapes
    break it. Python's repr picks a DOUBLE-quoted string for a word that holds
    an apostrophe, and SQLite reads a double-quoted token as an IDENTIFIER. And
    SQLite gives a backslash no escape meaning, so a word that holds one matches
    no WHEN, the CASE yields NULL for a NOT NULL column, and the INSERT aborts.
    """
    parts: list[str] = []
    params: list[Any] = []
    for column in carry:
        mapping = convert_columns.get(column)
        if mapping is None:
            parts.append(quote(column))
            continue
        whens = " ".join("WHEN ? THEN ?" for _ in mapping)
        parts.append(f"CASE {quote(column)} {whens} END AS {quote(column)}")
        for word, ordinal in mapping.items():
            params.append(word)
            params.append(ordinal)
    return ", ".join(parts), params


def rebuild_table(conn: sqlite3.Connection, ref: Reference, table: str,
                  colmaps: dict[str, dict[str, int]]) -> bool:
    """Bring one table up to the reference, and report whether it changed."""
    have = live_ddl(conn, table)
    if have is None:
        # A table this change ADDS. There is no old row to carry over, and
        # replay_dependents creates the indexes and the triggers it owns.
        conn.execute(ref.ddl[table])
        print(f"  {table}: created")
        return True
    if normalize(have) == ref.normalized[table]:
        return False

    info = conn.execute(f"PRAGMA table_info({quote(table)})").fetchall()
    names = [row[1] for row in info]
    declared = {row[1]: (row[2] or "").upper() for row in info}
    rows = conn.execute(f"SELECT COUNT(*) FROM {quote(table)}").fetchone()[0]

    # A column this change ADDS as NOT NULL with no DEFAULT has no value an old
    # row can supply, so NO row of this table carries over. The shape changed to
    # hold data the old rows never had, and a carried-over row would state a
    # condition its own empty columns contradict. The dry run reports this
    # before anything commits.
    unfillable = sorted(
        name for name, column in ref.columns[table].items()
        if name not in names and column.not_null and column.default is None
    )
    # Carry the columns BOTH schemas declare. A column this change adds takes
    # its DEFAULT, and a column this change drops stays behind with the old
    # table, which the rebuild then deletes.
    carry = [] if unfillable else [c for c in names if c in ref.columns[table]]
    # Convert the mapped columns the LIVE table still stores as TEXT. A column
    # this change ADDS holds no word, and a column an earlier run converted
    # already holds an ordinal -- neither one has a word to map.
    convert_columns = {c: m for c, m in colmaps.items()
                       if c in carry and declared.get(c) == "TEXT"}
    for column, mapping in convert_columns.items():
        refuse_unknown_values(conn, table, column, mapping)
    select, params = select_list(carry, convert_columns)

    old = quote(table + "__old")
    conn.execute(f"ALTER TABLE {quote(table)} RENAME TO {old}")
    # execute, never executescript: executescript COMMITS the open transaction
    # before it runs, which is exactly what this rebuild must not do.
    # ref.ddl[table] is a single CREATE TABLE statement.
    conn.execute(ref.ddl[table])
    if carry:
        columns_sql = ", ".join(quote(c) for c in carry)
        conn.execute(
            f"INSERT INTO {quote(table)} ({columns_sql}) SELECT {select} FROM {old}", params)
    # The rename moves the indexes and the triggers of this table onto the old
    # name, so they do not fire on the fresh rows. The drop then deletes them,
    # and replay_dependents puts them back.
    conn.execute(f"DROP TABLE {old}")
    if unfillable:
        print(f"  {table}: rebuilt, {rows} row(s) DROPPED -- the current table declares "
              f"{', '.join(unfillable)} NOT NULL with no default, and no old row states it")
    else:
        print(f"  {table}: rebuilt, {rows} row(s) carried over")
    return True


def replay_dependents(conn: sqlite3.Connection, ref: Reference) -> int:
    """Create every index and trigger whose live form differs from the reference.

    A rebuild drops the ones its own table held, and this puts them back. It
    also repairs one that sits on a table no rebuild touched, because this
    change rewrote the statement itself.
    """
    replayed = 0
    for name in ref.dependents:
        have = live_ddl(conn, name)
        if have is not None and normalize(have) == ref.normalized[name]:
            continue
        if have is not None:
            conn.execute(f"DROP {ref.kind[name].upper()} {quote(name)}")
        conn.execute(ref.ddl[name])
        replayed += 1
    if replayed:
        print(f"  {replayed} index(es) and trigger(s) replayed")
    return replayed


def verify(conn: sqlite3.Connection, ref: Reference) -> None:
    """Prove the live schema matches the migration, then check the data.

    Every object, not only the rebuilt tables: a rename drops the indexes that
    pointed at the old table, and this is what proves they came back. An object
    the live database holds and the reference does not stays where it is --
    goose's own goose_db_version table is one, and this run does not own it.
    """
    after = {name: normalize(sql) for name, sql in conn.execute(
        "SELECT name, sql FROM sqlite_master WHERE sql IS NOT NULL").fetchall()}
    missing = sorted(set(ref.normalized) - set(after))
    differing = sorted(n for n in set(ref.normalized) & set(after)
                       if ref.normalized[n] != after[n])
    if missing or differing:
        raise SystemExit(f"schema mismatch after rebuild: missing={missing} differing={differing}")
    broken = conn.execute("PRAGMA foreign_key_check").fetchall()
    if broken:
        raise SystemExit(f"foreign key check failed: {broken[:5]}")
    integrity = conn.execute("PRAGMA integrity_check").fetchone()[0]
    if integrity != "ok":
        raise SystemExit(f"integrity check failed: {integrity}")


def convert(db: Path, columns: dict, migration: Path, apply: bool) -> int:
    ref = reference_schema(migration)
    # isolation_level=None hands every transaction to this script. The default
    # mode opens one implicitly and COMMITS it before any statement sqlite3
    # classifies as DDL, which would break each rebuild into separately durable
    # pieces -- a failed run would then leave the database half-converted.
    conn = sqlite3.connect(db, isolation_level=None)
    conn.execute("PRAGMA foreign_keys = OFF")
    # ALTER TABLE ... RENAME normally REWRITES every foreign key in other tables
    # that pointed at the old name, which would leave eight referencing tables
    # pointing at `<table>__old` after the rename and at nothing after the drop.
    # legacy_alter_table is the documented way to rename without that rewrite,
    # and it is what makes the 12-step rebuild safe here.
    conn.execute("PRAGMA legacy_alter_table = ON")
    changed = 0
    try:
        conn.execute("BEGIN")
        for table in ref.tables:
            if rebuild_table(conn, ref, table, columns.get(table, {})):
                changed += 1
        replay_dependents(conn, ref)
        verify(conn, ref)
        if apply:
            conn.commit()
            print(f"  committed ({changed} table(s) rebuilt)")
        else:
            conn.rollback()
            print(f"  DRY RUN, rolled back ({changed} table(s) would be rebuilt)")
    finally:
        conn.execute("PRAGMA legacy_alter_table = OFF")
        conn.execute("PRAGMA foreign_keys = ON")
        conn.close()
    return changed


def identify(db: Path) -> tuple[dict, Path]:
    """Decide whether this is the hub database or the worker database.

    It asks the DATABASE, by comparing the tables it holds against each
    reference schema. The file NAME cannot answer: the hub path is whatever
    `storage.sqlite.path` says, so a legitimately configured `/data/leapmux.db`
    was refused outright, and a hub database whose name happened to start with
    "worker" took the worker reference. That second case was the dangerous one.
    The two schemas share no table, so every worker table was simply absent,
    `rebuild_table` created all fifteen of them from scratch, `verify` compared
    only reference objects and passed, and `--apply` committed a hub database
    carrying a full set of empty worker tables with its own enum words still
    unconverted.
    """
    live = sqlite3.connect(f"file:{db}?mode=ro", uri=True)
    try:
        present = {row[0] for row in live.execute(
            "SELECT name FROM sqlite_master WHERE type = 'table'")}
    finally:
        live.close()
    matches = [
        (label, columns, migration)
        for label, columns, migration in (
            ("hub", HUB_COLUMNS, HUB_MIGRATION),
            ("worker", WORKER_COLUMNS, WORKER_MIGRATION),
        )
        if present & set(reference_schema(migration).tables)
    ]
    if len(matches) != 1:
        found = ", ".join(label for label, _, _ in matches) or "neither"
        raise SystemExit(
            f"cannot tell whether {db} is the hub or the worker database: it matches {found}")
    label, columns, migration = matches[0]
    print(f"schema: {label}")
    return columns, migration


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("database", type=Path)
    ap.add_argument("--apply", action="store_true", help="commit; otherwise roll back")
    args = ap.parse_args()
    columns, migration = identify(args.database)
    print(f"{args.database}:")
    convert(args.database, columns, migration, args.apply)
    return 0


if __name__ == "__main__":
    sys.exit(main())
