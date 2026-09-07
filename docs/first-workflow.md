## A small change, with a before and after

You need to fix a few rows. First, save a backup. Make the change, then check what actually moved. Here's that whole workflow in dbterm, using a small SQLite database you can throw away afterwards.

This is useful when you're checking a seed script, testing an app action, or cleaning up development data. If you spend your day in a terminal, you can keep the query, results and backup in the same place.

## 1. Open a practice database

[Install dbterm](https://dbterm.shreyam1008.com.np/#install), then run `dbterm`. On the Dashboard, press `N`, choose SQLite, and use a new file called `dbterm-demo.sqlite` in a writable folder. Save and connect. Use a separate demo file for this walkthrough.

Press `Alt+Q` to focus the query editor. Run these statements one at a time with `Enter`:

```sql
CREATE TABLE tasks (id INTEGER PRIMARY KEY, title TEXT NOT NULL, status TEXT NOT NULL);
```

```sql
INSERT INTO tasks VALUES (1, 'Write the guide', 'todo'), (2, 'Check the release', 'todo'), (3, 'Fix the typo', 'done');
```

```sql
SELECT * FROM tasks ORDER BY id;
```

You should see three tasks. `Shift+Enter` adds a new line without running your query. `Ctrl+Space` opens SQL suggestions.

## 2. Get around without reaching for the mouse

Use `Alt+T` to focus Tables. Type `tasks`, then press `Enter` to open it. Press `Space` on the table to pin it for next time.

`Alt+R` focuses Results; `Alt+Q` takes you back to SQL. If you forget where something lives, `Ctrl+P` searches commands, objects and recent queries. These are the default shortcuts; your saved settings may differ.

## 3. Take a backup

Press `Alt+B` for an instant backup. Choose an absolute destination folder, such as `C:\dbterm-demo-backups` on Windows or a folder under your home directory on Linux/macOS. Wait for the run to finish successfully before continuing.

SQLite snapshot backups are built in. PostgreSQL and MySQL backups need their native dump tools installed. For recurring work, `Alt+K` opens Backup Center, where you can create a schedule and configure the background agent. The [backup guide](https://dbterm.shreyam1008.com.np/backup/) covers that setup and restore checks.

## 4. Set a checkpoint

Press `Alt+W` to open Change Profiler, then `N` to create an anchor. Call it **Before task update**, include the `tasks` table in the plan, and capture the baseline.

An anchor is your reference point for comparing data. Keep this first one small: capture and scan both read the selected rows.

## 5. Make a change and check it

Return to the query editor and run:

```sql
UPDATE tasks SET status = 'done' WHERE id = 1;
```

```sql
INSERT INTO tasks VALUES (4, 'Record the demo', 'todo');
```

Open Change Profiler again with `Alt+W`. Select your anchor and press `S` to scan. Open the report with `Enter`.

You should find one updated row—task 1 changed from `todo` to `done`—and one inserted row, task 4. The other two tasks should be unchanged. Press `F` when you're ready for the final scan and to finish the anchor; the compact report stays available.

The report shows observed changes, not who made them. It isn't an undo button or a continuous audit log. Your backup is the separate recovery artifact.

## Where this earns its place

- **Debugging:** capture an anchor, exercise a feature in your app, then inspect the affected rows.
- **Small data fixes:** find the table, back up, run a precise update, and review the result.
- **Repeat visits:** saved connections, pinned tables and query history save you setting everything up again.

For a PostgreSQL or MySQL server, you can also save the login without a database name and browse the databases that account can access. SQLite keeps this first walkthrough free of server setup.

That's enough for a first session. [Install dbterm](https://dbterm.shreyam1008.com.np/#install), try the four-row example, and use the [full guide](https://dbterm.shreyam1008.com.np/guide/) when you need more detail.
