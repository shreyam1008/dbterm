# dbterm Backup Center

This document is the operating and maintenance guide for dbterm backups. It
covers one-off backups, durable schedules, native background services,
retention, notifications, inspection, and restore.

## What the Backup Center protects

| Database | Backup format | Restore support |
| --- | --- | --- |
| PostgreSQL | Native custom archive from `pg_dump` | Previewed restore through official PostgreSQL clients |
| MySQL / MariaDB | Single-database SQL from `mysqldump` | Previewed SQL restore through official MySQL clients |
| SQLite | Consistent database snapshot | Guarded, staged snapshot or SQL restore |
| Turso / LibSQL | Single-transaction logical SQL export | Inspectable; virtual/FTS schemas fail closed; automatic restore is not enabled yet |
| Cloudflare D1 | Cloudflare native SQL export API | Inspectable; automatic restore is not enabled yet |

The source can be local or remote. For example, a saved PostgreSQL connection
to an AWS-hosted database can write backups to a local disk or an OS-mounted
network/object-storage volume. Native S3 uploads are not claimed in this
release.

Turso schema and data reads stay on one source transaction. dbterm rejects
virtual/FTS tables before publication because separately exporting their shadow
tables can create an unrestorable artifact. D1 backups use Cloudflare's own
export API instead of reconstructing rows through the SQL driver, then stream
the short-lived signed HTTPS download into private staging. Cloudflare may make
the D1 database temporarily unavailable while its native export runs.

## The normal workflow

1. Save and test the database connection in the dbterm Dashboard.
2. Open Backup Center with `Alt+K`, press `N`, then choose an existing saved
   database or add one. Alternatively, press `Ctrl+B` on the selected Dashboard
   connection to start with it preselected.
3. Choose a destination, schedule, and whether the policy is enabled.
4. Keep the storage, security, and notification defaults unless the backup has
   a specific operational requirement.
5. Save the job and install either the desktop or server backup agent.
6. Run the job once manually. Confirm its path, checksum, duration, and history
   entry before relying on the schedule.
7. Periodically test inspection and restore with a non-production target.

Instant backup remains available with `Alt+B` from any active workspace panel.
It uses the same engine-aware native dump and verification pipeline but does not
create a durable schedule. Press `F2` to choose a folder or type the path; `F3`
refreshes both destination and private-staging volume capacity.

### Backup Center controls

| Key | Action |
| --- | --- |
| `Ctrl+B` on Dashboard | Start a new job with the highlighted saved connection selected |
| `B` on Dashboard / `Alt+K` | Open Backup Center |
| `Alt+B` in a workspace | Create an instant backup of the active database |
| `N` | Choose a saved database or add a connection for a new job |
| `Enter` | Edit the highlighted job |
| `Ctrl+N` in the plan form | Add and select another saved database |
| `Space` | Enable or disable a timed schedule; manual plans stay on demand |
| `R` | Run the highlighted job now with live progress |
| `P` | Review and apply retention now |
| `H` | Open run, artifact, and notification history |
| `I` | Inspect a backup and enter the guarded restore flow |
| `A` | Inspect and manage desktop/server agent scopes and logs |
| `G` | Generate an age X25519 identity |
| `D` | Delete the job definition; completed artifacts remain |
| `F2` / `F3` in instant and plan forms | Choose a folder / refresh destination and staging capacity |

## Job form and defaults

The form is ordered by operational importance and uses progressive disclosure.

### Essential

- **Saved connection:** stable connection identity; renaming or reordering the
  Dashboard cannot silently redirect a job.
- **Job name:** used in history, logs, notifications, and optional filenames.
- **Destination:** an absolute local or mounted path. It can be typed or chosen
  with the native folder dialog when a desktop session is available.
- **Schedule and enabled state:** manual, interval, daily, or weekly.

The default policy uses Zstandard compression, a daily 02:00 local schedule,
missed-run catch-up, a 30-minute timeout, and conservative retention. Required
fields are at the top; naming, compression, encryption, retention, and email
are advanced sections.

### Timing

- **Manual:** runs only when requested from Backup Center or the CLI.
- **Interval:** every N minutes.
- **Daily:** once at an `HH:MM` time.
- **Weekly:** at `HH:MM` on selected weekdays.
- **Timezone:** `Local` or an IANA timezone such as `Asia/Kolkata`.
- **Catch up missed run:** after sleep, reboot, or downtime, run one overdue
  occurrence. It never replays every missed interval.

With catch-up disabled, an occurrence more than two minutes late is skipped and
the schedule advances to its next future time.

### Destination and capacity

Backup Center shows the selected volume and its available/capacity values when
the OS exposes them. The progress view reports the destination and live bytes
written. dbterm still needs temporary space in two places:

- private state storage for one native, usually uncompressed dump; and
- the selected destination for one in-progress wrapped artifact.

The folder chooser is a convenience, not a requirement. On a headless server,
over SSH, or when no supported desktop chooser is installed, type or paste the
absolute path. dbterm validates and creates the destination when the job is
saved.

### File names

The default filename is unique per run. Templates support:

- `{job}`
- `{connection}`
- `{database}`
- `{engine}`
- `{date}`
- `{time}`
- `{timestamp}`
- `{run}`

Path separators and unknown tokens are rejected. Existing files are never
silently replaced.

### Compression

- **Zstandard:** default; single worker and lower-memory mode.
- **gzip:** interoperable stream compression.
- **ZIP:** a single named backup entry.
- **None:** useful when an external system handles compression.

Compression and encryption stream through bounded buffers. The entire dump is
not loaded into memory.

### Encryption

Jobs can encrypt artifacts with an age X25519 public recipient. Generate an
identity with:

```bash
dbterm backup keygen
```

Only the public `age1...` recipient belongs in a job. Keep the private identity
separately from the backup destination and include it in the disaster-recovery
plan. Losing it makes encrypted artifacts unrecoverable.

### Retention

Retention can combine three ceilings:

- keep the latest N successful artifacts;
- remove artifacts older than N days; and
- cap the total recorded artifact size, pruning oldest successful artifacts
  first.

The newest successful artifact is always retained. Cleanup only considers
successful artifacts recorded for that exact job and still contained by that
job's destination. Before deletion, dbterm verifies regular-file identity,
size, and the recorded SHA-256 when available. A changed file, symlink, or path
outside the destination is refused rather than deleted.

Deleting a job, stopping the service, updating dbterm, or uninstalling dbterm
does not delete chosen backup artifacts.

Changing a retention policy affects later successful runs. To enforce it now,
review the ceilings and use Backup Center's prune action or explicit CLI
consent:

```bash
dbterm backup prune --yes <job-id-or-name>
```

## Progress and history

The live view uses determinate progress where a byte total is known and an
indeterminate activity bar otherwise. It shows:

- current phase: preflight, native dump, verification, wrapping, publication,
  retention, or notification;
- bytes produced and total bytes when known;
- elapsed time, current transfer rate, and estimated remaining time when those
  values are meaningful; and
- the last diagnostic message.

Native PostgreSQL/MySQL clients do not expose a reliable row-level percentage.
For those engines, dbterm reports live dump-file growth and elapsed time rather
than inventing a percentage. Wrapping can use the completed raw dump size for
an exact byte percentage.

Every durable run records status, timestamps, trigger, error, artifact path,
size, format, checksum, and verification state. Interrupted runs are reconciled
as failed after their lease expires so they do not stay “running” forever.

## Email notifications

Each job can notify:

- never;
- on failure only;
- on success only; or
- on both success and failure.

Gmail defaults are `smtp.gmail.com`, port `587`, with STARTTLS. Port `465` uses
implicit TLS. Custom SMTP servers, ports, senders, recipients, and TLS mode are
supported. Gmail normally requires 2-Step Verification and a dedicated app
password; use the full Gmail address as the username.

Notification delivery happens only after the run result is durably recorded.
An email failure is logged and shown in diagnostics but never changes a valid
backup into a failed backup. Passwords are redacted from errors and logs.

The SMTP app password is stored locally in the private backup catalog so the
headless agent can authenticate. It is not protected by the backup artifact's
age recipient and is not an OS-keyring secret. Protect the OS account and
dbterm state directory, use a dedicated/revocable app password, and rotate it
if the machine or account is compromised. Plain SMTP should only be used for a
trusted local relay; encrypted transport is the default.

Send a test without running a backup or changing the saved job:

```bash
dbterm backup notify-test <job-id-or-name>
```

## Background-agent modes

The agent is serialized and low-overhead. A global process lock prevents two
schedulers from executing the same catalog concurrently, while per-job leases
prevent manual and scheduled overlap.

### Desktop/user mode

This is the default and requires no administrator access.

| OS | Registration | Lifetime |
| --- | --- | --- |
| Linux | `systemd --user` | User manager; enable lingering for post-logout operation |
| macOS | `launchd` LaunchAgent | Logged-in user session |
| Windows | Task Scheduler logon task | Logged-in user session |

On Linux, `loginctl enable-linger <user>` lets the user manager start at boot
and remain after logout. That is often the cleanest server setup when permitted
by the administrator.

### Server/system mode

Server mode is an explicit elevated install. It registers at system boot and
uses the selected dbterm config, state, and log paths rather than silently
switching to an administrator's profile.

| OS | Registration | Elevation |
| --- | --- | --- |
| Linux | system `systemd` unit, configured to run as the invoking user | root/sudo required |
| macOS | `/Library/LaunchDaemons` LaunchDaemon with an explicit user | administrator required |
| Windows | boot-triggered Task Scheduler registration | Administrator required |

Permission failures explain the exact manager, definition path, and elevation
needed. There is no automatic fallback from server mode to desktop mode.

Install from Backup Center's **Agent** view, or use the CLI:

```bash
dbterm backup service install
dbterm backup service status --all
```

For server scope, first run `dbterm backup paths`, create and permission the
three directories for the intended runtime user, then execute the exact
elevated command shown by Backup Center. On Linux, for example:

```bash
sudo dbterm backup service install --system --run-as backupuser \
  --config-dir /home/backupuser/.config/dbterm \
  --state-dir /home/backupuser/.local/state/dbterm \
  --log-dir /home/backupuser/.local/state/dbterm/logs
```

macOS server mode also requires `--run-as`. Windows server mode runs as
LocalSystem, so it omits `--run-as` and its three paths must be local and
accessible to SYSTEM. A mapped drive in an interactive Windows account is not
a machine-service destination.

The manager records the exact executable path. Build or install a stable binary
first; disposable `go run` executables are rejected.

The foreground fallback is useful in containers or a supervisor you already
operate:

```bash
dbterm backup agent
```

## Service health

Backup Center separates four facts that are easy to confuse:

- whether a native registration exists;
- whether startup is enabled;
- whether the manager reports it running; and
- whether the dbterm heartbeat and global scheduler lock are healthy.

When available, status also shows PID, process name, uptime, resident memory,
last heartbeat, manager, scope, and registration detail. Process metrics are
diagnostic only; failure to read them never stops a backup.

## Logs and diagnostics

Routine agent logs are written to:

```text
<dbterm log directory>/dbterm-backup-agent.log
```

The active log and one archive are each bounded to 5 MiB. Backup Center can
show a tail with the exact path, and the CLI prints paths with:

```bash
dbterm backup paths
dbterm backup logs --lines 200
dbterm backup logs --previous
```

Native managers may also create exceptional stdout/stderr files in the same
directory. A good error includes the failed phase, database/client involved,
destination, and corrective action without printing database or SMTP secrets.

Useful checks:

```bash
dbterm backup status
dbterm backup service status --all
dbterm backup list
dbterm backup paths
dbterm backup logs --lines 200
```

## Inspect and restore

Inspection identifies gzip, Zstandard, single-entry ZIP, and age wrappers,
then detects PostgreSQL custom/tar/plain SQL, MySQL SQL, SQLite databases, or
SQLite SQL from bytes rather than trusting the extension. Misleading names
produce warnings.

```bash
dbterm backup inspect ./backup-file
dbterm backup inspect --identity ./age-identity.txt ./backup-file.age
```

Inspection unwraps at most three layers. Each decoded layer defaults to a 1 GiB
safety cap and uses private OS temporary files, not memory. Increase the cap
only for a trusted larger artifact and ensure temporary storage has enough
space:

```bash
dbterm backup inspect --max-decoded-gib 8 ./large-backup.zst
```

Restore is preview-first. The detected engine must match the selected saved
connection, explicit consent is required, and destructive clean mode requires
the exact database name or normalized SQLite path.

```bash
dbterm backup restore --connection staging --yes ./backup-file
```

PostgreSQL clean restore is opt-in. MySQL warns that non-transactional or
already-applied statements may remain after a client failure. SQLite restores
use a verified staging database and preserve a pre-restore copy.

## Native files and paths

Run `dbterm backup paths` for authoritative paths on the current machine.

| OS | Config | State/catalog | Logs |
| --- | --- | --- | --- |
| Linux | `${XDG_CONFIG_HOME:-~/.config}/dbterm` | `${XDG_STATE_HOME:-~/.local/state}/dbterm` | state + `/logs` |
| macOS | `~/Library/Application Support/dbterm` | same application-support tree | `~/Library/Logs/dbterm` |
| Windows | `%AppData%\dbterm` | `%LocalAppData%\dbterm` | state + `\logs` |

Saved database credentials remain in private `connections.json`. Jobs,
schedules, SMTP settings, leases, run history, checksums, and heartbeats live
in `backup/backups.db`. Raw dumps and temporary database-client credential
files are staged in the private state area and cleaned after use. Old crash
partials are cleaned on a later run.

## Reliability checklist

- Keep the dbterm binary and native database clients at stable paths.
- Run the first backup manually and inspect its history entry.
- Keep destination capacity above one raw dump plus one wrapped artifact.
- Use count, age, and/or size retention rather than an unbounded directory.
- Encrypt off-machine artifacts and store the age identity separately.
- Use a dedicated SMTP app password and failure notifications.
- Confirm the agent heartbeat, startup state, uptime, and recent log after OS
  updates or credential changes.
- Perform a real restore drill into an isolated target on a schedule.
- Monitor both successful backups and the absence of expected backups.

## Maintainer verification

Backup changes should pass unit tests, the race detector, vet, site validation,
and cross-builds for Linux, macOS, and Windows on amd64 and arm64. Platform
service definitions and folder-picker behavior need build-tagged tests; native
runtime testing is still required before claiming a platform-specific manager
change production-ready.
