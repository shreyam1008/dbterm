#!/usr/bin/env python3
"""Create disposable SQLite databases for interactive and performance review.

Uses Python's standard library. Never opens a user database or overwrites an
existing output directory. Generated files belong under the ignored .local/.
"""

import argparse
import json
import sqlite3
import time
from pathlib import Path


def create_database(path, row_count, table_count=0, wide=False):
    started = time.perf_counter()
    db = sqlite3.connect(path)
    try:
        db.executescript("""
            PRAGMA journal_mode=WAL;
            CREATE TABLE records (
                id INTEGER PRIMARY KEY AUTOINCREMENT,
                name TEXT NOT NULL, note TEXT, amount REAL, enabled BOOLEAN,
                created_at TEXT, metadata JSON, payload BLOB
            );
            CREATE TABLE keyless (label TEXT, value INTEGER);
            CREATE TABLE composite_keys (
                tenant TEXT, item INTEGER, note TEXT,
                PRIMARY KEY(tenant, item)
            ) WITHOUT ROWID;
            CREATE TABLE "odd [red] table" ("select" TEXT, "has space" TEXT);
            CREATE VIEW record_summary AS
                SELECT enabled, COUNT(*) AS total FROM records GROUP BY enabled;
        """)
        labels = ["plain", "नेपाल", "日本語", "café e\u0301", "👩🏽‍💻", "[red]literal[-]", "line\nbreak", "tab\there"]
        notes = [None, "", "short", "before\x00after", "line one\nline two", "x" * (131072 if wide else 2048)]
        batch_size = 1000
        for first in range(0, row_count, batch_size):
            rows = []
            for index in range(first, min(first + batch_size, row_count)):
                rows.append((
                    labels[index % len(labels)], notes[index % len(notes)],
                    (index % 20001 - 10000) / 100, index % 2,
                    f"2026-10-{index % 28 + 1:02d}T12:34:56Z",
                    json.dumps({"index": index, "tags": ["demo", "Unicode ✓"]}, ensure_ascii=False),
                    bytes(range(256)) if index % 10 == 0 else (b"" if index % 10 == 1 else None),
                ))
            db.executemany("INSERT INTO records(name,note,amount,enabled,created_at,metadata,payload) VALUES(?,?,?,?,?,?,?)", rows)
            db.commit()
        db.executemany("INSERT INTO keyless VALUES(?,?)", [("duplicate", 1), ("duplicate", 1), (None, None)])
        db.executemany("INSERT INTO composite_keys VALUES(?,?,?)", [("a", 1, "same key per tenant"), ("b", 1, "second tenant")])
        db.execute('INSERT INTO "odd [red] table" VALUES(?,?)', ("[green]literal[-]", "quote ' and \""))
        for index in range(table_count):
            db.execute(f'CREATE TABLE catalog_{index:03d}(id INTEGER PRIMARY KEY, label TEXT)')
            db.execute(f'INSERT INTO catalog_{index:03d} VALUES(1,?)', (f"table {index}",))
        if wide:
            db.execute("CREATE TABLE wide_grid(" + ",".join(f"col_{i:03d} TEXT" for i in range(150)) + ")")
            db.executemany("INSERT INTO wide_grid VALUES(" + ",".join("?" for _ in range(150)) + ")", ([f"column {i}" for i in range(150)] for _ in range(100)))
        db.commit()
        assert db.execute("SELECT COUNT(*) FROM records").fetchone()[0] == row_count
        assert db.execute("PRAGMA integrity_check").fetchone()[0] == "ok"
        db.execute("PRAGMA wal_checkpoint(TRUNCATE)")
    finally:
        db.close()
    return {"path": str(path), "rows": row_count, "extra_tables": table_count, "wide": wide,
            "bytes": path.stat().st_size, "generation_seconds": round(time.perf_counter() - started, 3)}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", type=Path, default=Path(".local/review/fixtures"))
    parser.add_argument("--million", action="store_true", help="Also create a one-million-row fixture (roughly 500 MB)")
    args = parser.parse_args()
    output = args.output.resolve()
    output.mkdir(mode=0o700, parents=True, exist_ok=False)
    matrix = [("empty", 0, 0, False), ("small", 100, 0, False), ("medium", 10000, 0, False),
              ("large", 100000, 0, False), ("catalog", 100, 200, False), ("wide", 1000, 0, True)]
    if args.million:
        matrix.append(("million", 1000000, 0, False))
    manifest = []
    connections = []
    for name, rows, tables, wide in matrix:
        result = create_database(output / f"{name}.sqlite3", rows, tables, wide)
        manifest.append(result)
        connections.append({"id": f"review-{name}", "name": f"Review · {name} · {rows:,} rows", "type": "sqlite", "file_path": result["path"]})
        print(json.dumps(result), flush=True)
    config_dir = output / "config"
    config_dir.mkdir(mode=0o700)
    (config_dir / "connections.json").write_text(json.dumps({"connections": connections}, indent=2) + "\n")
    (output / "manifest.json").write_text(json.dumps(manifest, indent=2) + "\n")
    print(f"Use DBTERM_CONFIG_DIR={config_dir} with a separate DBTERM_STATE_DIR and DBTERM_LOG_DIR.")


if __name__ == "__main__":
    main()
