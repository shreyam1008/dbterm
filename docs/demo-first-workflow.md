# dbterm first-workflow recording plan

Status: rebuilt 7 September 2026 as a 54-second animated walkthrough, with large example data, typed SQL, an animated before/after comparison and a real dbterm v0.11.0 report capture. The video is caption-led with original instrumental audio. Illustrative data panels simplify the workflow; they are not screenshots of the app. It is embedded in `/first-workflow/`; the revision is being published through the Website workflow. Editable production source is in the owner's code2video repository under `src/videos/dbterm-workflow/`; the final wording/timing there supersedes the initial spoken draft below.

The sample was exercised in an isolated profile: SQLite backup completed, its original three rows and integrity were checked, the two SQL statements executed, and Change Profiler reported one insert and one update. No real user databases were used. See `docs/first-workflow.md` for the reproducible SQL.

Aim for about 90 seconds after preparing the three-row SQLite demo. Show only the disposable database, with a clean terminal and no personal connection names. Keep text readable at mobile width. Provide captions and the companion guide link. Leave actual backup/capture completion visible; label any time cuts. No simulated footage or unsupported speed claims.

| Time | Screen action | Spoken draft |
| --- | --- | --- |
| 0–10s | Open tasks; show its three rows | “Here's a small database fix in dbterm. I'll back it up, change a task, then check the result.” |
| 10–22s | Focus Tables, find tasks, pin it; open Ctrl+P | “Type to find a table. Space pins it. If I forget a command, I can search here.” |
| 22–38s | Alt+B; choose local folder; wait for success | “First, a backup. This demo uses SQLite, so there's no separate dump tool to install.” |
| 38–53s | Alt+W, N; name anchor and capture tasks | “Now I'll save a before point for this table. dbterm calls it an anchor.” |
| 53–67s | Run the UPDATE and INSERT from the guide | “Mark one task done. Add another. Those should be the only changes.” |
| 67–83s | Scan anchor; inspect inserted row and before/after status | “Here's the new row, and here's todo becoming done. This is a comparison, not an undo button.” |
| 83–90s | Show guide/install address | “Try it with the sample database in the guide. The code and downloads are on the dbterm site.” |

Suggested title: **Back up, change, compare: a short dbterm walkthrough**

Suggested description: “I maintain dbterm, a terminal database workbench. This walkthrough uses a tiny SQLite database to show keyboard navigation, an instant backup and a before/after comparison. Steps and sample SQL: https://dbterm.shreyam1008.com.np/first-workflow/”

Do not publish the description link until the page is deployed. Re-record any step that differs in the installed release. Reddit remains owner-published only.

