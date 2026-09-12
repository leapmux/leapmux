#!/usr/bin/env python3
"""Convert a live LeapMux SQLite database's enum columns from words to proto ordinals.

The shipped migration is edited IN PLACE (this project ships no upgrade path
before release), so goose sees version 1 already applied and does nothing for a
database that predates the change. This script is that one-off conversion.

It rebuilds only the tables that hold an enum column, because SQLite cannot
alter a CHECK constraint. Each rebuild runs inside ONE transaction with foreign
keys deferred, and the run ends with an integrity check, a foreign-key check,
and a comparison of the resulting schema against a database built from the
current migration -- so a rebuild that dropped an index or mistyped a column
fails here rather than at the next boot.

Usage:  migrate-enum-columns.py <hub.db|worker.db> [--apply]
Without --apply it reports what it would change and writes nothing.
"""
import argparse
import re
import sqlite3
import sys
from pathlib import Path

REPO = Path(__file__).resolve().parents[2]
HUB_MIGRATION = REPO / "backend/internal/hub/store/sqlite/db/migrations/00001_initial.sql"
WORKER_MIGRATION = REPO / "backend/internal/worker/db/migrations/00001_initial.sql"

# table -> {column: {stored word: proto ordinal}}. A word absent from a map is
# an unknown value: the run stops rather than guessing an ordinal for it.
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


def goose_up(path: Path) -> str:
    """The migration's Up section, which is the schema this build expects."""
    text = path.read_text()
    up = text.split("-- +goose Up", 1)[1].split("-- +goose Down", 1)[0]
    # The worker migration wraps its triggers in StatementBegin/End markers,
    # which are goose directives rather than SQL.
    return re.sub(r"^-- \+goose Statement(Begin|End)\s*$", "", up, flags=re.M)


def reference_schema(migration: Path) -> tuple[dict[str, str], dict[str, list[str]]]:
    """The schema this build expects, from a database built by the migration.

    Returns the normalized DDL of every object, and the VERBATIM index and
    trigger statements grouped by the table they belong to. The rebuild replays
    the reference statements rather than the ones the live database holds: an
    index whose predicate this change rewrote (idx_revocation_events_session_revoked
    now seeks `kind = 2`) would otherwise come back in its old form, and a
    partial index that no query matches is a silent full scan.
    """
    ref = sqlite3.connect(":memory:")
    ref.executescript(goose_up(migration))
    rows = ref.execute(
        "SELECT name, type, tbl_name, sql FROM sqlite_master WHERE sql IS NOT NULL").fetchall()
    ref.close()
    schema = {name: normalize(sql) for name, _, _, sql in rows}
    owned: dict[str, list[str]] = {}
    for _, kind, table, sql in rows:
        if kind in ("index", "trigger"):
            owned.setdefault(table, []).append(sql)
    return schema, owned


def normalize(sql: str) -> str:
    """DDL with comments and whitespace removed, so only the statement compares."""
    sql = re.sub(r"--[^\n]*", "", sql)
    return re.sub(r"\s+", " ", sql).strip()


def convert(db: Path, columns: dict, migration: Path, apply: bool) -> int:
    reference, owned = reference_schema(migration)
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
        for table, colmaps in columns.items():
            have = conn.execute(
                "SELECT sql FROM sqlite_master WHERE type='table' AND name=?", (table,)).fetchone()
            if not have:
                print(f"  {table}: absent, skipped")
                continue
            if normalize(have[0]) == reference[table]:
                print(f"  {table}: already converted")
                continue
            # Refuse a value no mapping covers rather than invent an ordinal.
            for column, mapping in colmaps.items():
                unknown = conn.execute(
                    f"SELECT DISTINCT {column} FROM {table} "
                    f"WHERE {column} NOT IN ({','.join('?' * len(mapping))})",
                    tuple(mapping)).fetchall()
                if unknown:
                    raise SystemExit(
                        f"{table}.{column} holds values no mapping covers: "
                        f"{[u[0] for u in unknown]}")
            names = [r[1] for r in conn.execute(f"PRAGMA table_info({table})")]
            select = ", ".join(
                (f"CASE {c} " + " ".join(f"WHEN {v!r} THEN {o}" for v, o in colmaps[c].items())
                 + f" END AS {c}") if c in colmaps else c
                for c in names)
            rows = conn.execute(f"SELECT COUNT(*) FROM {table}").fetchone()[0]
            conn.execute(f"ALTER TABLE {table} RENAME TO {table}__old")
            # execute, never executescript: executescript COMMITS the open
            # transaction before it runs, which is exactly what this rebuild
            # must not do. reference[table] is a single CREATE TABLE statement.
            conn.execute(reference[table])
            conn.execute(
                f"INSERT INTO {table} ({', '.join(names)}) SELECT {select} FROM {table}__old")
            # Drops the old table and, with it, the indexes it still owned.
            conn.execute(f"DROP TABLE {table}__old")
            for sql in owned.get(table, []):
                conn.execute(sql)
            print(f"  {table}: rebuilt, {rows} row(s) converted")
            changed += 1

        # Every object, not only the rebuilt tables: a rename drops the indexes
        # that pointed at the old table, and this is what proves they came back.
        after = {name: normalize(sql) for name, sql in conn.execute(
            "SELECT name, sql FROM sqlite_master WHERE sql IS NOT NULL").fetchall()}
        missing = sorted(set(reference) - set(after))
        differing = sorted(n for n in set(reference) & set(after) if reference[n] != after[n])
        if missing or differing:
            raise SystemExit(f"schema mismatch after rebuild: missing={missing} differing={differing}")
        broken = conn.execute("PRAGMA foreign_key_check").fetchall()
        if broken:
            raise SystemExit(f"foreign key check failed: {broken[:5]}")
        integrity = conn.execute("PRAGMA integrity_check").fetchone()[0]
        if integrity != "ok":
            raise SystemExit(f"integrity check failed: {integrity}")
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


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("database", type=Path)
    ap.add_argument("--apply", action="store_true", help="commit; otherwise roll back")
    args = ap.parse_args()
    name = args.database.name
    if name.startswith("hub"):
        columns, migration = HUB_COLUMNS, HUB_MIGRATION
    elif name.startswith("worker"):
        columns, migration = WORKER_COLUMNS, WORKER_MIGRATION
    else:
        raise SystemExit(f"cannot tell whether {name} is the hub or the worker database")
    print(f"{args.database}:")
    convert(args.database, columns, migration, args.apply)
    return 0


if __name__ == "__main__":
    sys.exit(main())
