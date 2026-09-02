# dbterm Backup Center

This is the operator and maintenance guide for dbterm backup generation,
independent copies, encryption, scheduling, retention, inspection, and restore.

dbterm uses one binary and one lightweight backup agent per local catalog. A
producer creates a database recovery artifact. A copy job then moves an already
completed artifact to another local directory, an SFTP vault, or from an rclone
source into a local vault. Backup and copy runs have separate history and health.

## Protection boundary

dbterm currently creates a **full database backup on every backup run**. It does
not claim incremental database backup, point-in-time recovery, WAL/binlog
archiving, block deduplication, or replication.

Copy jobs are incremental only at artifact granularity: each run scans portable
completion manifests, skips exact artifact IDs already present with the same
size and SHA-256, and transfers every missing artifact oldest-first. This saves
network work without changing the full-backup recovery model.

Application folders can be included in the same recovery point. They are copied
into a self-contained dbterm bundle; they are not maintained as an incremental
file mirror.

The practical protection states are:

| State | Meaning |
| --- | --- |
| Backup failed | No new verified recovery point was published |
| Local backup complete | The producer has one verified artifact and completion manifest |
| Copy failed | The local backup may still be valid, but the independent copy is unhealthy |
| Copy complete | The destination has a separately verified artifact and manifest |
| Agent healthy | Automation is running; this alone does not prove that any recovery point exists |
| Restore tested | A backup was actually restored and checked; basic artifact verification is not the same fact |

## Supported database backups

| Database | Generation format | Restore boundary |
| --- | --- | --- |
| PostgreSQL | One-database `pg_dump` custom archive | Guarded restore through PostgreSQL client tools |
| MySQL / MariaDB | One-database SQL from `mysqldump` | Guarded SQL restore through MySQL client tools |
| SQLite | Consistent database snapshot | Guarded staged snapshot or SQL restore |
| Turso / LibSQL | Single-transaction SQLite-compatible SQL | Inspectable and restorable into an isolated SQLite target; dbterm does not orchestrate a write back to the hosted service |
| Cloudflare D1 | Cloudflare native SQL export API | Inspectable and restorable into an isolated SQLite target; dbterm does not orchestrate a write back to D1 |

The saved database connection may point to another machine or cloud service.
The machine running dbterm owns generation and initially publishes to an
absolute local path or an OS-mounted path visible to that agent identity.
Direct rclone backup generation is disabled; use a copy job after local
publication.

### Engine consistency details

- PostgreSQL uses `pg_dump` custom format with UTF-8, no ownership, and no
  privileges. Passwords use a private temporary password file rather than a
  command-line argument.
- MySQL uses `--single-transaction --quick --routines --events --triggers` and a
  private temporary defaults file. `--single-transaction` gives a consistent
  InnoDB snapshot while normal writes continue, but schema-changing statements
  such as `ALTER`, `DROP`, or `TRUNCATE` should not run during the dump.
- SQLite uses `VACUUM INTO` for a consistent snapshot.
- Turso/LibSQL reads schema and rows in one transaction. Virtual/FTS tables are
  refused because exporting their shadow tables independently can produce an
  unrestorable artifact.
- D1 uses Cloudflare's export API and streams the returned download into private
  staging. Cloudflare may temporarily make the database unavailable while its
  native export runs.

## Normal setup

1. Save and test the database connection in the dbterm Dashboard.
2. Open Backup Center with `Alt+K`, or press `Ctrl+B` on the selected Dashboard
   connection.
3. Create a backup plan with an absolute local or mounted destination.
4. Add any application folders from the plan's **Included folders** action.
5. Choose one or more daily times, an interval, or a weekly schedule.
6. Generate and separately store an age recovery identity if the backups should
   be encrypted.
7. Run the backup once manually and inspect its artifact, adjacent
   `.dbterm.json` manifest, checksum, and history.
8. Open **Copies** and add a local, SFTP, or rclone-pull copy if one machine is
   not sufficient protection.
9. Keep timed and after-success copies disabled. Use the read-only endpoint test
   for diagnostics, then run one real copy manually; enable automation only
   after that run transfers and verifies a real artifact successfully.
10. Install the user or system backup agent and periodically perform a restore
    drill into an isolated target.

Instant backup remains available with `Alt+B` from an active workspace. The CLI
equivalent is:

```bash
dbterm backup create --connection production --destination /srv/dbterm-backups
```

Instant backups use the same native dump, basic structural verification,
bounded streaming wrappers, final-byte SHA-256, immutable publication, and
portable sidecar pipeline. They do not create a durable schedule or run-history
row, and the instant CLI does not accept application file sets.

## Backup Center controls

Main Backup Center:

| Key | Action |
| --- | --- |
| `N` | Create a backup plan |
| `C` | Open independent copy jobs |
| `Enter` | Open actions for the selected plan |
| `R` | Run the selected backup now |
| `I` | Inspect a local backup and enter guarded restore |
| `H` | Open backup activity/history |
| `A` | Inspect or manage the native agent/service |
| `Space` | Pause or resume a timed backup plan |

Selected-plan actions include editing the plan, running it, viewing activity,
managing copies, managing included folders, applying retention, and deleting
only the plan definition. Deleting a plan does not delete completed artifacts.

Copies view:

| Key | Action |
| --- | --- |
| `N` | Add a supported copy topology |
| `Enter` / `E` | Edit a topology supported by the wizard |
| `T` | Run a non-destructive endpoint test |
| `R` | Run the copy and measure real end-to-end throughput |
| `I` | Choose a recorded recovery point, then stage, verify, inspect, or restore it |
| `H` | View copy runs and artifact results |
| `P` | Preview, then optionally apply, copy retention |
| `Space` | Pause or resume a timed copy |
| `D` | Delete the copy-job definition; copied files remain |

Included folders view uses `N` to add, `Enter`/`E` to edit, and `D` to remove a
folder from future backup policy. Existing bundles are never rewritten.

The current TUI copy wizard handles local-to-local, local-to-SFTP push,
backup-plan-to-SFTP after-success push, SFTP-to-local pull, and
rclone-to-local pull. For a local destination it exposes normal-folder,
already-mounted, OS-managed, and managed Linux block-device volume modes. The
volume fields include mount point, sentinel filename and identity, plus the
managed-Linux UUID, filesystem type, optional label and mount options,
warmup/cooldown, and spindown controls. The same wizard progressively exposes
copy-specific SMTP policy, TLS, sender, recipients, and credentials only when
email delivery is enabled, and can send a test message without running a copy.

## Backup plan defaults and schedules

The normal default is Zstandard compression, a daily 02:00 local-time schedule,
missed-run catch-up, a 30-minute timeout, and retention of the latest 14
successful artifacts.

Schedules support:

- manual execution;
- an interval from 5 minutes through 365 days;
- daily execution at one or more wall-clock times;
- weekly execution on selected weekdays at one or more wall-clock times; and
- `Local` or an IANA timezone such as `Asia/Kolkata`.

For example, one plan can run at both `01:00` and `13:00`; duplicate times are
normalized instead of creating duplicate jobs. The bundled timezone database is
used for deterministic service operation. A nonexistent local time in a DST
spring-forward gap is skipped, and an ambiguous fall-back time runs once.

After sleep, reboot, or downtime, catch-up runs at most one overdue occurrence.
It does not replay every missed tick. With catch-up disabled, an occurrence more
than two minutes late is skipped and the next future occurrence is persisted.
Manual and scheduled runs use the same job lease and cannot overlap.

The durable backup-plan schedule is configured in the TUI. Copy schedules can
also be created from the CLI with repeatable `--at` values.

## Backup artifact pipeline

A normal run performs these boundaries:

1. Resolve the saved connection by stable ID and validate the plan.
2. Verify the destination and private staging context.
3. Generate the full engine-native database payload without exposing its
   password on the command line.
4. Run engine-specific basic structural verification and reject empty or
   suspicious payloads.
5. If file sets exist, capture them into private staging and build a dbterm
   bundle around the verified database payload.
6. Stream compression and optional age encryption through bounded buffers.
7. Compute SHA-256 over the final bytes that will be copied or restored.
8. Sync and publish the artifact without replacing an existing final name.
9. Publish the strict sidecar manifest last as the completion signal.
10. Durably finish the backup run.
11. Run enabled `after_success` copy jobs with independent copy runs.
12. Apply producer-local retention.
13. Attempt notification after durable state exists.

Cancellation before immutable publication removes private staging. If
cancellation or an I/O error races with publication, history records whether
the result is complete, artifact-only, or uncertain and preserves possible
recovery bytes for review.

### File names

The default filename contains a unique run suffix. Templates support:

- `{job}`
- `{connection}`
- `{database}`
- `{engine}`
- `{date}`
- `{time}`
- `{timestamp}`
- `{run}`

Path separators and unknown tokens are rejected. A completed final name is
never silently replaced.

## Application file sets and dbterm bundles

A database-only job keeps its engine-native payload and existing restore
compatibility. Adding one or more file sets changes future output to a
self-contained deterministic tar-based `dbterm_bundle` before the configured
compression and encryption wrappers are applied.

Manage file sets from Backup Center's **Included folders** view or the CLI:

```bash
dbterm backup files list production

dbterm backup files add \
  --label profile-photos \
  --root /srv/registration/profile_pics \
  --include "**" \
  --exclude "*.tmp" \
  --exclude "thumbs.db" \
  production

dbterm backup files remove --yes production profile-photos
```

`--include` and `--exclude` are repeatable slash-separated globs. `**` is
supported only as a complete path segment. Exclusions win after inclusion. A
file set is required by default; `--optional` changes failure into omission of
the whole set with a visible portable warning. `--replace` updates an existing
same-label policy. Changes apply only to future runs.

Safety rules are deliberately strict:

- the root is normalized to an exact absolute path and stays only in the local
  producer catalog;
- roots, directories, and files that are symlinks or Windows reparse points are
  refused;
- path traversal, non-portable paths, and non-regular files are refused;
- only relative paths are written to the bundle;
- each selected file is copied and hashed in private staging, then rechecked;
- the complete selected membership and metadata are scanned again after
  capture; and
- a changing required set fails the backup, while a changing optional set is
  atomically omitted with a warning.

This detects common live-folder changes but is not an application transaction,
VSS snapshot, or filesystem snapshot. The database and application folders are
not one cross-resource point-in-time transaction. Quiesce the application
outside dbterm when that stronger consistency is required.
Successful live-folder summaries therefore use the portable consistency value
`best-effort`; dbterm never describes this capture as a stable snapshot.

Current bundle limits include 64 file sets per job, 256 combined include and
exclude patterns per set, 100,000 files across all sets, 64-byte portable
labels, 4096-byte portable paths/patterns, and a 32 MiB internal bundle
manifest. Arbitrary pre/post shell hooks are not supported.

The internal bundle manifest records the embedded database format plus each
file's relative path, size, and SHA-256. It never records the original absolute
application roots.

## Compression and encryption

Compression options are:

- **Zstandard**: default, single worker, lower-memory encoder;
- **gzip**: interoperable stream compression;
- **ZIP**: one named backup entry; and
- **none**: useful when another controlled layer handles compression.

The encryption choice is deliberately **Off** or **age X25519**. There is no
cipher, KDF, password-encryption, or custom cryptography menu. dbterm compresses
first and then encrypts in the age v1 format, and the artifact SHA-256 covers
the final ciphertext.

Generate a no-clobber recovery identity and record its public recipient:

```bash
dbterm backup keygen --output /secure/recovery/dbterm-age-identity.txt
```

Put only the printed `age1...` public recipient in the backup plan. The
producer does not need the private identity to create encrypted backups. Store
at least one private-identity copy separately from the encrypted artifact
storage; losing every identity makes those backups unrecoverable.

Prove that a stored identity matches the configured recipient without creating
or changing a backup:

```bash
dbterm backup keycheck \
  --identity /secure/recovery/dbterm-age-identity.txt \
  --recipient age1...
```

`keycheck` encrypts and decrypts only a disposable in-memory challenge. It is a
key-recovery check, not an artifact restore drill.

On POSIX, identities must be regular, non-symlink files with mode `0600` or
stricter. On Windows, generated identities use a protected DACL. Identity
contents and the public recipient are not written to portable manifests.

Inspect or restore an encrypted artifact by supplying the private identity:

```bash
dbterm backup inspect --identity /secure/recovery/dbterm-age-identity.txt backup.sql.zst.age
```

## Portable completion manifests

Every successful dbterm backup publishes a strict sidecar beside the artifact:

```text
orders_mysql_20260903_010000_run-abc.sql.zst
orders_mysql_20260903_010000_run-abc.sql.zst.dbterm.json
```

The schema-v1 JSON records:

- artifact, run, job, and stable producer IDs;
- creation time and dbterm version;
- database engine and payload format;
- compression and encryption scheme;
- final byte count and SHA-256;
- `passed` / `basic-structural` verification state;
- portable file-set summaries; and
- warnings such as an omitted optional set.

It excludes database host/user/name, passwords, connection strings, SMTP
secrets, SSH keys, age recipients, age identities, and original file-set roots.
The decoder rejects unknown or duplicate fields, missing required arrays,
trailing JSON, unsupported schema versions, malformed checksums, symlinks, and
manifests larger than 1 MiB.

The artifact is synced and published first. The manifest is synced and
published last. Copy scanners consider the sidecar—not filename age or an
existing same-named file—the completion signal.

Publication history distinguishes:

| State | Meaning |
| --- | --- |
| `complete` | Artifact and manifest reached immutable final names and publication checks completed |
| `artifact-only` | Artifact is final but the completion manifest is absent; scanners ignore it |
| `uncertain` | Publication may have crossed an irreversible boundary, but final presence or durability could not be proved |

The sidecar is not digitally signed. A pinned SSH connection protects the
transport endpoint and SHA-256 protects exact bytes, but neither authenticates
a manifest that a compromised producer intentionally forged. For an encrypted
artifact without the private identity, copy verification can prove the
ciphertext size/SHA-256 and age envelope, not the claimed inner database. A
real restore drill remains the strongest recovery test.

Legacy artifacts without a sidecar can still be inspected locally. Copy jobs
ignore them; legacy `.sha256` files are not automatically converted into
dbterm manifests.

## Copy jobs

A copy job owns transfer only. It never reconnects to the database or recreates
the dump.

| Source | Destination | Mode / owner | Current support |
| --- | --- | --- | --- |
| Local directory or local backup plan | Local directory | push or pull semantics | Yes |
| Local directory or local backup plan | SFTP directory | producer-owned push | Yes |
| SFTP directory | Local directory | vault-owned pull | Yes |
| rclone prefix | Local directory | vault-owned pull | Yes |
| Local directory | rclone prefix | producer-owned push | No |
| SFTP | SFTP | either | No |
| SCP shell command | any | either | No; `ssh://` is only an alias for the SFTP subsystem |

Backup generation itself also cannot publish directly to rclone. Generic
rclone finalization can overwrite a destination object after a preflight race,
so dbterm refuses to claim portable create-only publication where the backend
cannot prove it.

### Copy CLI

List and inspect definitions and history:

```bash
dbterm backup copy list
dbterm backup copy status
dbterm backup copy status vrindavan-to-ct400
```

Create an after-success local copy bound to a backup plan:

```bash
dbterm backup copy create \
  --name "Producer local vault" \
  --mode push \
  --source-job production \
  --destination /srv/dbterm-vault \
  --trigger after-success \
  --keep-last 14
```

Create a producer-owned SFTP push:

```bash
dbterm backup copy create \
  --name "Production to vault" \
  --mode push \
  --source-job production \
  --destination sftp://dbterm_copy@vault.example/srv/dbterm-vault \
  --identity /etc/dbterm/ssh/vault_ed25519 \
  --host-key SHA256:PINNED_FINGERPRINT \
  --trigger after-success
```

Create a vault-owned SFTP pull at two daily times:

```bash
dbterm backup copy create \
  --name "Producer to CT400" \
  --mode pull \
  --source sftp://dbterm_read@producer.example/srv/dbterm-published \
  --destination /mnt/backup_hdd/registration_backups \
  --identity /etc/dbterm/ssh/producer_ed25519 \
  --host-key SHA256:PINNED_FINGERPRINT \
  --trigger timed \
  --at 02:30 --at 14:30 \
  --timezone Asia/Kolkata
```

Create a pull from an existing rclone remote:

```bash
dbterm backup copy create \
  --name "Object storage to local vault" \
  --mode pull \
  --source rclone://offsite/dbterm/production \
  --destination /srv/dbterm-vault \
  --trigger timed --every 12h
```

Jobs are disabled by default. Every timed or after-success copy must remain
disabled until a successful manual `copy run` transfers and verifies at least
one real artifact. `copy test` is a read-only diagnostic; it does not prove the
transfer or publication path and is not sufficient reason to enable automation.
Do not use `--enable` at creation for an unproved route. Use this sequence:

```bash
dbterm backup copy test vrindavan-to-ct400
dbterm backup copy run vrindavan-to-ct400
dbterm backup copy enable vrindavan-to-ct400
```

Use `disable`, `prune`, and `delete --yes` for the corresponding lifecycle
operations. Deleting a copy job never deletes source or destination files.

Copy creation also supports repeatable `--format` manifest filters,
`--expected-freshness`, count/age/byte retention ceilings, timeouts, and bounded
retry settings. The TUI exposes producer-ID and source-job-ID filters. An
enabled reverse or duplicate route with overlapping filters is rejected so the
same artifact stream cannot have both push and pull owners.

### Copy execution and verification

A copy run:

1. scans direct children for strict `.dbterm.json` completion manifests;
2. validates optional producer, source-job, and format filters;
3. sorts every missing artifact oldest-first;
4. matches existing recovery points by artifact ID plus size and SHA-256, never
   by filename alone;
5. reserves artifact size plus a 64 MiB safety margin for local destinations;
6. streams through bounded buffers and hashes the bytes;
7. runs the requested lightweight format check;
8. syncs local staging where applicable;
9. publishes artifact first and manifest last without replacing final names;
10. durably records each destination copy; and
11. applies only that copy job's retention policy.

The public CLI creates jobs with `sha256+format`. The TUI can explicitly choose
`sha256+format` or `sha256`; it does not offer size-only verification. Format
verification means a lightweight outer-envelope or native-header check. An
uncompressed dbterm bundle receives a full internal size/SHA-256 layout check;
a compressed or encrypted artifact needs full inspection/decryption for deeper
payload verification.

Local and pull transfers use private destination-local partial files. SFTP push
uses exclusive final artifact creation, but publishes no completion manifest
until upload and remote reread verification succeed, so an interrupted orphan
is not recovery-ready. A later run can reconcile an exact artifact-only orphan.
Completed names are never overwritten.

Transient network failures use bounded exponential backoff with jitter. The
defaults are three attempts, a two-second initial delay, and a one-minute cap.
There is no byte-range or chunk-resume protocol in the current implementation;
a retry repeats or safely reconciles its own artifact.

When no new artifacts are missing, the scan is a successful no-op. If
`--expected-freshness` is configured, the run records a warning when no producer
artifact exists or the newest one is too old.

### SSH/SFTP security

SFTP endpoints use:

```text
sftp://service-user@host/absolute/path
```

An explicit port may be included. `ssh://` is accepted as a spelling alias but
still starts the SFTP subsystem; dbterm never invokes a remote shell or SCP.

Every SFTP endpoint requires:

- an explicit service username;
- an absolute path to a dedicated private identity file;
- a canonical pinned host key in `SHA256:<base64>` form; and
- a remote path below the SFTP root, with no parent traversal.

Password authentication, passwords in URLs, trust-on-first-use, and
`InsecureIgnoreHostKey` are refused. A changed host key blocks the connection
before transfer. The private key must be a regular non-symlink file, no larger
than 1 MiB, and mode `0600` or stricter on POSIX. Encrypted/passphrase-protected
SSH identities are not supported by the unattended copy agent; use a dedicated
unencrypted key protected by OS permissions and narrowly scoped remote access.

For pull, give the vault identity read/list permission only where practical.
For push, scope the producer identity to its destination directory and
create/read/delete rights needed by publication and configured retention. The
current client dials TCP directly; OpenSSH config and `ProxyCommand` integration
are not implemented.

### rclone pull

rclone is supported only as a read-only copy source. dbterm lists one prefix
level, reads strict sidecars, snapshots source size and modification time,
streams the artifact with `rclone cat`, hashes it locally, and rechecks both
artifact and sidecar source versions before publication.

dbterm does not modify or retain the rclone source. Retention belongs to the
local pull destination. rclone must already be installed and configured for the
agent's OS identity.

### Endpoint tests and speed

`dbterm backup copy test` is intentionally non-destructive:

- local tests validate real directories and report destination capacity;
- SFTP tests verify the pinned host key, authenticate, start SFTP, list the
  configured root, and report connect/list latency; and
- rclone tests list the source and count objects and completed manifests.

The test does not upload and remove a probe object. In particular, an SFTP push
test does not prove remote create-only permission, and no endpoint test claims
transfer throughput. The first real `copy run` proves the write path and reports
actual copied bytes divided by total run duration as B/s, KiB/s, or MiB/s. Keep
automatic copies disabled until such a manual run succeeds with a real artifact.

## Destination volume safety

An optional destination-volume policy can bind a local copy destination to a
positive sentinel identity. Configure it with CLI flags or in the TUI copy form
for a route with a local destination.

All three modes require:

- `--volume-mount-point`: an absolute, non-root mount point containing the
  destination;
- `--volume-id`: an exact non-secret 8-256 character token without whitespace;
  and
- a regular sentinel file at the mount root containing exactly that token.

The default sentinel filename is `.dbterm-volume-id`; override it with
`--volume-sentinel-file`. dbterm never creates the sentinel, initializes the
volume, or creates an identity token automatically.

### Already mounted and OS managed

```bash
dbterm backup copy create \
  --name "Verified mounted vault" \
  --mode pull \
  --source /srv/dbterm-published \
  --destination /mnt/backup_hdd/dbterm \
  --volume-mode already-mounted \
  --volume-mount-point /mnt/backup_hdd \
  --volume-id ct400-registration-vault
```

`already-mounted` and `os-managed` are verify-only modes. dbterm never mounts,
unmounts, syncs, or powers off the volume. Before transfer it verifies a real
non-symlink mount-point directory, a real destination directory contained under
it, and the exact small regular sentinel. It repeats the identity check after
transfer. Use `os-managed` to communicate that systemd automount, Windows,
SMB/NFS, or another OS facility owns lifecycle.

### Managed Linux block device

```bash
dbterm backup copy create \
  --name "Sleeping USB vault" \
  --mode pull \
  --source /srv/dbterm-published \
  --destination /mnt/backup_hdd/dbterm \
  --volume-mode managed-linux-block-device \
  --volume-mount-point /mnt/backup_hdd \
  --volume-id ct400-registration-vault \
  --volume-uuid 1111-2222 \
  --volume-filesystem ext4 \
  --volume-label DBTERM_VAULT \
  --volume-mount-option errors=remount-ro \
  --volume-warmup 5s \
  --volume-cooldown 5s \
  --volume-spindown
```

Managed mode is Linux-only. It resolves the configured filesystem UUID with
`blkid`, verifies UUID/type and optional label, checks existing mount state with
`findmnt`, and mounts only the exact block device at the exact configured mount
point. Safe options `rw,nodev,nosuid,noexec` are added automatically; options
that re-enable `dev`, `suid`, or `exec`, or appear to contain credentials, are
rejected.

A durable destination-volume lease prevents two dbterm copy jobs from
unmounting the same identified volume underneath one another. dbterm tracks
whether this run performed the mount. It only runs `sync -f`, `umount`, and
optional `udisksctl power-off` when it mounted the volume itself and still owns
the lease. A pre-existing mount is verified but left mounted. An unmount or
spindown failure becomes a warning and never invalidates a verified copy.

The destination directory and sentinel must already exist on the intended
filesystem. A missing or wrong mount, UUID, filesystem, label, destination
directory, or sentinel fails closed before transfer so dbterm does not fill an
ordinary directory on the system disk.

Provision `blkid`, `findmnt`, `mount`, `sync`, `umount`, and, when requested,
`udisksctl` plus narrowly scoped host privileges outside dbterm. The agent is
not automatically elevated and dbterm never formats, partitions, initializes,
repairs, or fscks a disk. Managed Windows disk lifecycle is not implemented.

`backup copy test` remains a non-destructive endpoint test; the real
`backup copy run` is what claims the volume lease and exercises managed mount,
identity recheck, retention, and release.

## Independent retention

Backup generation and every copy job own separate retention policies. One side
never broadly cleans the other side's history.

Each policy can combine:

- keep the latest N successful recovery points;
- remove recovery points older than N days; and
- cap recorded bytes, pruning the oldest successful recovery points first.

The newest verified recovery point is always retained. Retention runs only
after successful publication, or through explicit preview/apply. The CLI copy
preview is the default:

```bash
dbterm backup copy prune vrindavan-to-ct400
dbterm backup copy prune --yes vrindavan-to-ct400
```

Producer-local retention considers only successful artifacts recorded for that
exact backup job and contained by its destination. It validates regular-file
identity, size, SHA-256, manifest identity, and containment. The completion
manifest is removed first so scanners stop seeing the pair as published. Files
are captured to deterministic same-directory quarantine names and reverified
before deletion; changed captures are preserved for investigation.

Copy retention applies to local destinations and exact SFTP push destinations.
It considers only complete, unpruned artifacts durably recorded for that copy
job. SFTP retention re-reads and hashes the exact remote artifact and sidecar,
renames each to a contained quarantine name, re-verifies, then removes it. It
does not run a broad remote `find -delete`. An rclone pull never deletes its
source; its local destination uses local retention.

A missing, replaced, changed, symlinked, or out-of-scope file is refused rather
than deleted. Retention failure is a warning separate from the validity of the
new artifact or copy. Grandfather-father-son monthly/yearly retention is not
implemented.

## Notifications and health warnings

Backup plans can notify never, on failure, on success, or on both. Gmail
defaults to `smtp.gmail.com:587` with STARTTLS; port 465 uses implicit TLS.
Plain SMTP is permitted only for a deliberately selected trusted local relay.

Notification is attempted after terminal run state is durable. SMTP failure is
recorded separately and never changes a valid backup into a failed backup.
Passwords and connection secrets are redacted from diagnostic errors.

The SMTP app password must remain available to the headless agent, so it is
stored in the private local backup catalog as part of the job—not in an OS
keyring and not protected by the backup artifact's age encryption. Protect the
OS account and dbterm state directory, use a dedicated revocable app password,
and rotate it if the host is compromised.

Test a backup plan's mail settings without running a backup:

```bash
dbterm backup notify-test production
```

Copy runs have independent notification state in the durable model and SMTP
delivery never rewrites copy validity. Expected-freshness and retention
warnings are recorded in copy history. Configure a copy's policy and SMTP
settings in Backup Center's copy add/edit wizard, then use **Send Test Email**
before relying on unattended alerts. The app password is masked in the form,
preserved when an existing copy is edited without changing it, stored only in
the private local catalog, and redacted from diagnostics. The CLI intentionally
has no SMTP password flag. Reusable SMTP profiles, recovery-after-failure mail,
daily summaries, and an external no-heartbeat alert are deferred.

## Agent, concurrency, and services

One `dbterm backup agent` owns one local backup catalog and schedules both
backup and copy jobs. Closing the TUI does not stop the native service.

The protections are independent:

1. A kernel-backed process lock permits one agent for the catalog and reports
   the owning PID.
2. Per-job leases prevent manual and scheduled runs of the same backup or copy
   job from overlapping. Expired interrupted runs are reconciled as failed.
3. A destination-volume lease protects a configured removable volume across
   copy jobs.
4. Database snapshot semantics such as MySQL `--single-transaction` protect
   database consistency; they are not process locks.

The agent executes I/O-heavy jobs serially. It checks the catalog every 30
seconds by default, writes a bounded heartbeat, and opens database, SSH, SMTP,
and rclone connections only while work requires them. The interval can be
changed for a foreground supervisor with `dbterm backup agent --poll`, with a
minimum of one second.

### Desktop/user service

| OS | Registration | Lifetime |
| --- | --- | --- |
| Linux | `systemd --user` | User manager; enable lingering for operation after logout |
| macOS | LaunchAgent | Logged-in user session |
| Windows | Task Scheduler logon task | Logged-in user session |

### Server/system service

| OS | Registration | Runtime identity |
| --- | --- | --- |
| Linux | system systemd unit | Explicit restricted user |
| macOS | LaunchDaemon | Explicit user |
| Windows | boot-triggered Task Scheduler task | LocalSystem |

Server installation is an explicit elevated action and uses the exact config,
state, and log paths supplied by the operator. It does not silently switch to
the administrator/root profile.

```bash
dbterm backup paths
dbterm backup service install
dbterm backup service status --all
```

Example Linux system scope:

```bash
sudo dbterm backup service install --system --run-as backupuser \
  --config-dir /home/backupuser/.config/dbterm \
  --state-dir /home/backupuser/.local/state/dbterm \
  --log-dir /home/backupuser/.local/state/dbterm/logs
```

Windows LocalSystem cannot use an interactive user's mapped drive. Use a local
machine-visible volume, UNC path exposed to that runtime identity, or a copy
transport configured for the service account. Build/install dbterm at a stable
path before service registration; disposable `go run` executables are refused.

Service status separates registration, startup enablement, manager-reported
running state, heartbeat, and scheduler-lock ownership. When available it also
shows PID, process name, uptime, resident memory, scope, and definition path.

## Progress and history

Backup progress reports preflight, dump, verification, file capture, wrapping,
publication, copy, retention, and notification boundaries. PostgreSQL/MySQL
clients do not expose a dependable row percentage, so dbterm reports staged
byte growth and elapsed time rather than inventing one. Wrapping and transfer
show determinate byte progress when a total is known.

Backup history records artifact path, manifest path, final-byte size/SHA-256,
format, verification and publication state, retention outcome, and notification
outcome. Copy history separately records trigger, discovery count, already
present count, bytes copied, required verification, per-artifact source and
destination, publication state, pruning, warnings, and SMTP outcome.

Useful commands:

```bash
dbterm backup list
dbterm backup status
dbterm backup copy list
dbterm backup copy status
dbterm backup service status --all
dbterm backup logs --lines 200
```

## Inspect and restore

Inspection detects age, gzip, Zstandard, and single-entry ZIP wrappers, then
identifies PostgreSQL custom/tar/plain SQL, MySQL SQL, SQLite database/SQL, or a
dbterm bundle from bytes rather than the filename. An adjacent sidecar, when
present, must match size, SHA-256, engine/format, encryption, and wrappers that
can be observed from the bytes.

```bash
dbterm backup inspect ./backup-file
dbterm backup inspect --identity ./age-identity.txt ./backup-file.age
dbterm backup inspect --max-decoded-gib 8 ./large-backup.zst
```

Inspection unwraps at most three compression/encryption layers. Each decoded
layer defaults to a 1 GiB limit and uses private temporary files instead of
loading the artifact into memory.

### Inspecting or restoring a recorded copy

```bash
dbterm backup copy inspect vrindavan-to-ct400
dbterm backup copy inspect vrindavan-to-ct400 --artifact artifact_...
dbterm backup copy inspect encrypted-copy --identity ./age-identity.txt
```

Without `--artifact`, the command deterministically selects the newest
completed, unpruned copied recovery point. It re-reads the durable copy's
sidecar, binds it to the catalog artifact ID/size/SHA-256, streams the exact
artifact into a private local directory, verifies its lightweight format, runs
the ordinary byte-based inspection, then removes only the exact staged files
and directory. Unexpected staging changes are preserved rather than recursively
deleted.

For a push job the destination may be remote SFTP, so `copy inspect` downloads
it temporarily. For pull jobs the normal destination is already local. The
CLI command is inspection-only: the private stage is removed when it exits, and
there is no direct `dbterm backup copy restore` CLI command.

In the TUI Copies view, select a job and press `I`. dbterm lists every
completed, unpruned destination artifact owned by that copy job, newest first,
with its timestamp, size, verification strength, and short artifact ID. Choose
the exact recovery point to stage and reverify before dbterm opens the ordinary
inspection and guarded restore flow. This works for local copies, remote SFTP push
copies, and the local destination copies produced by SFTP or rclone pulls.
Choosing **Restore…** from the inspection result enters the existing guarded
restore flow; private staging remains available for that flow and is cleaned up
when it ends.

### Guarded database and file restore

Restore always inspects first, requires a compatible saved target, prints a
plan, and requires `--yes`. Clean mode additionally requires the exact database
name or normalized SQLite path:

```bash
dbterm backup restore \
  --connection isolated-staging \
  --yes \
  ./backup-file

dbterm backup restore \
  --connection isolated-staging \
  --mode clean \
  --confirm-clean staging_database \
  --yes \
  ./backup-file
```

PostgreSQL clean restore is opt-in. MySQL warns that non-transactional or
already-applied statements may remain if its client fails. SQLite database-file
restore requires clean mode, uses a verified staging database, and preserves a
pre-restore copy.

A dbterm bundle restores its database only by default. Select each desired file
set explicitly and map it to a new isolated root:

```bash
dbterm backup restore \
  --connection isolated-staging \
  --restore-files profile-photos=/srv/restore/profile_pics \
  --restore-files documents=/srv/restore/documents \
  --yes \
  ./registration.dbterm.zst.age
```

The TUI restore form uses semicolon-separated mappings in **File targets**, for
example `profile-photos=/srv/restore/photos;documents=/srv/restore/docs`.

File restore defaults to no-clobber. `--overwrite-files` permits atomic
replacement of existing regular files only. Symlinks, reparse points,
non-regular targets, path traversal, reserved Windows device names, filesystem
roots, overlapping target roots, duplicate labels, and collisions with the
backup or SQLite database path are refused. Defaults limit selected file sets
to 100,000 files and 10 GiB; change them deliberately with
`--max-file-set-files` and `--max-file-set-gib`.

All selected file paths are preflighted before the database client starts. The
database is restored first, then selected files are published one by one. File
publication is atomic per file, but database plus multiple files are not one
cross-resource transaction. If file publication fails after the database
restore, the error explicitly reports that boundary.

Restore-drill results are not yet stored as a separate durable history type.
Record drill date, target, row-count checks, and representative file hashes in
the deployment's operational log.

## Failure behavior

| Failure | Behavior |
| --- | --- |
| Second agent | Kernel lock refuses it and reports ownership |
| Same job triggered twice | Job lease permits one run; the other trigger is refused/coalesced |
| Dump or basic verification fails | No completion manifest; no successful recovery point |
| Required file set is missing, unsafe, changing, or empty | Backup fails before publication |
| Optional file set has the same problem | Whole set is omitted with a manifest/history warning |
| Cancel before publication | Child work stops and private staging is removed |
| Cancel/error at publication boundary | Complete or possible artifact is preserved as artifact-only/uncertain for review |
| Missing sidecar | Copy scanner treats artifact as incomplete |
| Network interruption | No destination completion manifest; retry/reconciliation is limited to this artifact |
| Host-key mismatch | Connection is blocked before transfer |
| Wrong/missing volume sentinel | Copy stops before writing |
| Destination lacks local capacity | Copy fails before staging that artifact and keeps the newest recovery point |
| Several source artifacts are missing | They are processed oldest-first |
| Same name or conflicting artifact ID | Existing final object is never overwritten; mismatch fails |
| SMTP fails | Backup/copy validity remains unchanged; mail error is separate |
| Retention sees changed data | Deletion is refused and changed quarantine is preserved |
| Clock differs across hosts | Identity and checksum decide equality; modtime alone does not |

An artifact-only or uncertain publication is not automatically promoted to a
successful backup. Review its exact path, size, SHA-256, and any adjacent
sidecar, then inspect or quarantine it. Do not hand-author a sidecar or rewrite
history to claim success.

## Native paths, secrets, and logs

Run `dbterm backup paths` for authoritative paths on the current machine.

| OS | Config | State/catalog | Logs |
| --- | --- | --- | --- |
| Linux | `${XDG_CONFIG_HOME:-~/.config}/dbterm` | `${XDG_STATE_HOME:-~/.local/state}/dbterm` | state + `/logs` |
| macOS | `~/Library/Application Support/dbterm` | same application-support tree | `~/Library/Logs/dbterm` |
| Windows | `%AppData%\dbterm` | `%LocalAppData%\dbterm` | state + `\logs` |

Saved database credentials remain in private `connections.json`. Backup plans,
copy jobs, SMTP settings, leases, history, checksums, producer identity, and
heartbeats live in `backup/backups.db`. Protect both locations as secrets.

Private POSIX files use mode `0600` and directories use `0700`. Windows private
state uses a protected DACL for the runtime identity, SYSTEM, and built-in
Administrators. dbterm applies private/no-clobber semantics to its catalog,
credential files, raw dump staging, bundle staging, wrapper staging, copy
staging, inspection/restore staging, publication stages, and generated age
identities. It cannot retroactively secure an arbitrary operator-supplied
directory.

The destination namespace is a security boundary. Use a directory where
untrusted users cannot rename, replace, or delete entries. Per-file private
permissions do not make an untrusted writable parent safe.

Agent logs are written to:

```text
<dbterm log directory>/dbterm-backup-agent.log
```

The active log and one archive are each bounded to 5 MiB.

```bash
dbterm backup logs --lines 200
dbterm backup logs --previous
```

## Current limitations and deferred work

- No incremental database chain, PITR/WAL/binlog orchestration, replication,
  block-level deduplication, or bespoke chunk store.
- File sets are full captures inside each selected recovery point, not an
  incremental photo/document mirror.
- No rclone push or direct rclone backup generation; rclone is a pull source.
- No SCP execution; `ssh://` means SFTP. No OpenSSH config/ProxyCommand support.
- No byte-range copy resume. Retries re-transfer or reconcile only an exact
  artifact-only destination.
- No arbitrary pre/post hooks, application quiescing, VSS, LVM/ZFS snapshot, or
  filesystem repair.
- Managed mount lifecycle is Linux-only. Its destination-volume modes and fields
  are available through CLI flags and the TUI copy wizard.
- Copy SMTP settings are configurable and testable in the TUI, but reusable
  SMTP profiles, recovery notices, daily summaries, low-space thresholds, and
  no-heartbeat mail are deferred. The CLI intentionally has no password flag.
- No grandfather-father-son monthly/yearly retention.
- The CLI can stage and inspect a recorded copy, but it has no direct
  `backup copy restore` command. TUI Copies `I` stages local, SFTP, and
  rclone-derived copies and can continue into the existing guarded restore flow.
- Restore-drill outcomes are not yet a durable history type.
- Legacy `.sha256` sidecars are not first-class copy completion manifests.
- Portable manifests are checksummed but not signed.
- No central control plane, distributed transaction, or automatic migration of
  legacy schedules and artifacts.

## Reliability checklist

- Keep dbterm and required native database/rclone/Linux volume tools at stable
  paths visible to the service identity.
- Run the first backup and first copy manually, and keep automatic copy triggers
  disabled until the real copy transfers and verifies an artifact successfully.
- Confirm both artifact and `.dbterm.json` sidecar exist at every expected
  location.
- Use age for off-machine confidentiality and test the separately stored
  identity with `backup keycheck`.
- Keep enough producer staging/destination capacity for the native dump, bundle,
  and wrapped artifact; keep vault capacity above the artifact plus safety
  margin.
- Pin SFTP host keys and use one restricted service identity per responsibility.
- Use sentinel identity for a destination where a missing mount could redirect
  writes to the system disk.
- Configure count, age, and/or byte retention; preview before manual cleanup.
- Monitor recent successful backup time and independent copy freshness, not just
  the service heartbeat.
- Test notification delivery after credential or SMTP changes.
- Restore into an isolated database/directory and compare representative row
  counts and file hashes on a defined schedule.

## Maintainer verification

Backup changes should pass focused unit tests, the full unit suite, the race
detector where supported, `go vet`, documentation/site validation, and
Linux/macOS/Windows amd64/arm64 build gates. Native SFTP servers, database
clients, system service managers, Linux mount privileges, removable hardware,
and Windows ACL/reparse behavior still require platform integration testing
before a deployment-specific production claim.
