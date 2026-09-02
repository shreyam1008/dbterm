# dbterm Backup Center

This document is the operating and maintenance guide for dbterm backups. It
covers one-off backups, durable schedules, portable completion manifests,
native background services, retention, notifications, inspection, and restore.

## What the Backup Center protects

| Database | Backup format | Restore support |
| --- | --- | --- |
| PostgreSQL | Native custom archive from `pg_dump` | Previewed restore through official PostgreSQL clients |
| MySQL / MariaDB | Single-database SQL from `mysqldump` | Previewed SQL restore through official MySQL clients |
| SQLite | Consistent database snapshot | Guarded, staged snapshot or SQL restore |
| Turso / LibSQL | Single-transaction logical SQL export | Inspectable; virtual/FTS schemas fail closed; automatic restore is not enabled yet |
| Cloudflare D1 | Cloudflare native SQL export API | Inspectable; automatic restore is not enabled yet |

The database source can be local or remote. For example, a saved PostgreSQL
connection to an AWS-hosted database can write backups to a local disk or an
OS-mounted volume on the machine running dbterm. New backup-generation
destinations must currently be absolute local or mounted-local paths.

### Current protection boundary

The portable manifest is the foundation for producer/vault copies, but the
current catalog still owns backup-generation jobs only. One job creates one
artifact at one absolute local or OS-mounted destination. New rclone generation
fails closed because generic rclone finalization cannot provide an atomic
create-only final-name operation and can overwrite an object after a preflight
check. Existing rclone job and run records remain visible for migration and
manual inspection, but they cannot be saved again or run until their generation
destination is changed to local.

The current release does **not** yet provide a first-class `CopyJob`/`CopyRun`,
vault-owned SSH/SFTP pull, independent push health, multiple daily wall-clock
times, application file bundles, managed removable-disk lifecycle, or remote
restore selection. A future copy-job layer may use rclone where its backend can
meet the copy contract; that does not weaken the generation fail-closed rule.
Do not disable an existing PullMount-style service until the copy-job slice has
repeated parity tests. Backup Center calls the current destination one copy and
explicitly says that an extra copy is not configured.

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
6. Run the job once manually. Confirm its artifact, adjacent `.dbterm.json`
   completion manifest, checksum, duration, and history entry before relying on
   the schedule.
7. Periodically test inspection and restore with a non-production target.

Instant backup remains available with `Alt+B` from any active workspace panel.
It uses the same engine-aware native dump, verification, checksum, and
completion-manifest pipeline but does not create a durable schedule or catalog
history row. Press `F2` to choose a folder or type an absolute local/mounted
path; `F3` refreshes destination and private-staging capacity.

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
- **Destination:** an absolute local path or an OS-mounted volume. It can be
  typed or chosen with the native folder dialog when a desktop session is
  available. A UNC/NFS/SMB volume must already be mounted or otherwise exposed
  as a normal machine-visible filesystem path for the agent's OS identity.
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

Backup Center shows destination capacity when the OS exposes it. The progress
view reports the destination and bytes written. dbterm also needs private
temporary space for the native dump and completed wrapped artifact.

Rclone generation is intentionally disabled. A generic sequence that checks a
final name, uploads a partial object, and runs `rclone moveto` has a race:
another writer can create the final object after the check, and `moveto` may
replace it. `--immutable` does not turn that sequence into a portable atomic
create-if-absent operation across rclone backends. dbterm therefore rejects
`rclone://...` when creating, editing, or running a generation job instead of
claiming no-clobber publication it cannot guarantee.

The folder chooser is a convenience, not a requirement. On a headless server,
over SSH, or when no supported desktop chooser is installed, type or paste an
absolute local or mounted-local path. dbterm creates the folder when the job is
saved. Use an existing independently verified copy mechanism for off-machine
protection until the durable copy-job layer is available.

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

The encryption choice is deliberately small: **Off** or **age X25519**. There
is no cipher or KDF menu. Encrypted writes use the age v1 file format with one
X25519 public recipient; Zstandard remains the normal compression default.
Generate an identity with:

```bash
dbterm backup keygen
```

Only the public `age1...` recipient belongs in a job. Keep the private identity
separately from the backup destination and include it in the disaster-recovery
plan. Losing it makes encrypted artifacts unrecoverable. The artifact checksum
is over the final ciphertext, so transport verification never needs the private
identity. The portable manifest records `age-x25519-v1` but never records the
recipient, a recipient-derived identifier, an identity, or a connection secret.

On POSIX systems, `dbterm backup keygen` creates the identity with mode `0600`.
Inspection refuses a symlink, a non-regular or empty identity file, and any
identity file with group or other permission bits; an imported identity must
therefore be `0600` or stricter. On Windows, a newly generated identity uses a
protected DACL rather than relying on POSIX mode emulation.

### Completion manifests and copy truth

Every backup created through Backup Center, `dbterm backup create`, or a saved
job publishes a sidecar next to the final artifact:

```text
orders_mysql_20260903_010000_run-abc.sql.zst
orders_mysql_20260903_010000_run-abc.sql.zst.dbterm.json
```

The strict schema-v1 JSON records stable artifact/run/job/producer identities,
creation time, dbterm version, engine, format, wrappers, final byte count,
SHA-256, basic structural verification result, and warnings. `verification`
is `passed` only after that engine check, while `verification_level` is
`basic-structural`; this is not a substitute for a periodic restore drill. The
manifest does not include a host, database name, user, password, connection
string, age recipient, or private identity. Unknown fields, unsupported schema
versions, malformed checksums, oversized files, symlinked manifests, and
trailing JSON are rejected.

The sidecar is not signed. Treat its producer identity, engine, format,
compression, and encryption fields as unsigned producer assertions, not as
proof of authorship. For a locked age artifact, dbterm can independently match
the ciphertext's size and SHA-256, detect the outer age envelope, and require
the sidecar to describe age encryption. It cannot validate the claimed database
engine, inner format, or compression without the private identity. After
decryption, inspection detects the payload and wrapper stack from the bytes and
requires those observations to match the sidecar. That content check still does
not turn the manifest into a producer signature.

The artifact is published first and the manifest is published last. Local
publication fsyncs the completed bytes and containing directory. A future copy
scanner can therefore use the sidecar as the completion signal instead of
guessing from filename age. Run history distinguishes these publication states:

| State | Meaning | Recovery/copy status |
| --- | --- | --- |
| `complete` | Artifact and completion manifest reached their immutable final names and their publication checks completed | Successful recovery point; eligible as a completion signal |
| `artifact-only` | The artifact is final, but the manifest is absent or its finalization could not be confirmed | Failed run and orphan candidate; copy scanners ignore it |
| `uncertain` | Publication crossed or may have crossed an irreversible boundary, but final presence, size, or durability could not be confirmed | Failed run; not recovery-ready until a human investigates |

An orphan is deliberately preserved so a failure after the irreversible
artifact boundary does not destroy potentially useful bytes. Review the failed
run's exact path, publication state, recorded size, and SHA-256. Inspect the
local candidate and any adjacent sidecar. For a legacy rclone run, use rclone
itself to list and download the exact final object and sidecar before inspection.
Do not hand-author a sidecar or mark the failed run successful. Quarantine or
remove the candidate after review, then rerun the job to create a new supported
local artifact/manifest pair. Instant backups have no durable history row, so
retain the candidate path shown by the error dialog or CLI before closing it.
There is currently no automatic orphan-promotion/reconciliation command.

Backup Center keeps copy claims intentionally conservative:

- a recorded local success is rechecked for a regular artifact, recorded size,
  and any catalog-recorded completion manifest, then shown as a local copy
  present with the explicit warning that its checksum was not re-read;
- a legacy recorded rclone artifact is labeled as historical remote state whose
  size was checked by the older publication path, with availability not
  rechecked; it is not shown as a current or checksum-verified vault copy; and
- scheduler or agent health is automation readiness, never evidence that a
  recovery copy exists.

Inspection remains compatible with legacy artifacts that have no sidecar. When
an adjacent sidecar is present, inspection always requires its size and SHA-256
to match. If the payload is unlocked, it also requires engine, format,
encryption, and wrapper descriptions to match the decoded bytes; while locked,
only the observable outer age description can be checked.

### Retention

Retention can combine three ceilings:

- keep the latest N successful artifacts;
- remove artifacts older than N days; and
- cap the total recorded artifact size, pruning oldest successful artifacts
  first.

The newest successful artifact is always retained. Cleanup only considers
successful artifacts recorded for that exact job and still contained by that
job's destination. New artifacts and their completion manifests are pruned as
one pair: dbterm verifies the sidecar's catalog identity and checksum, removes
the completion signal first, re-verifies the artifact, and only then removes
the artifact. A local file is atomically moved to a deterministic,
artifact-derived same-directory quarantine name and re-verified there before it
is deleted. The stable capture name lets a later run safely reconcile a crash;
a changed capture is preserved there for manual review. Before deletion, dbterm
verifies size and the recorded SHA-256 when available. A changed file, manifest,
symlink, or path outside the destination is refused rather than deleted.

Retention for legacy rclone jobs also fails closed. Generic rclone cannot
conditionally delete the exact remote object version dbterm verified, so a
replacement could win the gap between verification and deletion. Preserve
those objects and use a backend-specific, version-aware retention policy until
the copy-job layer can enforce an equivalent conditional-delete contract.

A retention refusal does not invalidate the newly published backup. The warning
is stored on that terminal run before notification is attempted and appears in
activity/run details. A configured success, failure-only, or both policy can
therefore deliver a warning email even though the backup status remains
`succeeded`; the message explicitly separates valid backup creation from failed
storage cleanup. If updating history itself fails, live progress reports that
separate persistence problem and notification still uses the in-memory warning.

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

Every durable run records status, timestamps, trigger, error, artifact and
manifest paths, sizes and checksums, format, verification state, publication
state, and retention/notification outcomes. Interrupted runs are reconciled
as failed after their lease expires so they do not stay `running` forever.

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

Inspection and restore currently accept a local file. To inspect an artifact
from a legacy rclone run, download it first and then inspect the local copy:

```bash
rclone copyto offsite:dbterm/orders.dump.zst ./orders.dump.zst
dbterm backup inspect ./orders.dump.zst
```

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

Guarded temporary files and directories use no-clobber random names; named
private files use create-new semantics or have their permissions tightened. On
POSIX, files use mode `0600` and private directories use `0700`. On Windows,
dbterm installs and verifies a protected DACL granting the current runtime
identity, `SYSTEM`, and built-in Administrators while rejecting broad inherited
access. This path protects the default backup state directory and catalog
files, native dump and database-client credential files, artifact/manifest
publication stages, decoded inspection and restore temporary files, producer-ID
staging, and newly generated age identities. It does not retroactively secure
an operator-supplied file or directory. Protect the dbterm state tree and run
the agent as the intended restricted OS account.

The destination directory is a security boundary. The writability probe proves
that dbterm can create a file at preflight time; it does not prove that the
directory is private or remains unchanged. No-clobber publication, regular-file
and symlink checks, same-file checks, size/hash checks, and rechecks before
retention deletion reduce time-of-check/time-of-use risk, but they cannot make
a namespace writable by an untrusted account safe. Use a destination that
untrusted users cannot rename, replace, or delete, and treat a writable shared
directory as untrusted even when each staged file has private permissions.

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
