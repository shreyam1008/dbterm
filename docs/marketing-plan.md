# dbterm marketing, domain, and search plan

Status: working plan  
Owner: Shreyam Adhikari (`@shreyam1008`)  
Baseline: dbterm v0.6.4, 15 August 2026

## The decision

Use **`dbterm.shreyam1008.com.np` as the primary public URL**, while continuing to host the site on GitHub Pages.

Do not run the GitHub Pages URL and the custom domain as two independently indexed copies. Configure the custom domain on the existing Pages site so one hostname is canonical and the old `shreyam1008.github.io/dbterm/` URLs redirect to it. This gives dbterm its own durable product address while retaining the trust and personal association of `shreyam1008.com.np`.

Migration completed on 16 August 2026. The domain is verified in GitHub, the DNS-only CNAME points to GitHub Pages, the site builds at `/`, HTTPS is enforced, and the legacy GitHub Pages URL redirects to the custom domain.

Why this choice:

- `dbterm.shreyam1008.com.np` reads as a real product owned by Shreyam, not a temporary documentation path.
- The existing personal site already identifies Shreyam and links to dbterm, so the subdomain strengthens a real relationship instead of inventing a separate anonymous brand.
- GitHub Pages remains the free, reliable host; the custom domain is an identity and canonical-URL layer, not a hosting rewrite.
- A dedicated `dbterm.com.np` or unrelated new domain is not needed now. A keyword in a domain is not a ranking shortcut, and another domain would split identity and maintenance.

## Current position

### What is already strong

- One fast static site with HTTPS, mobile layouts, crawlable HTML, accessible navigation, unique page titles and descriptions, favicons, Open Graph/Twitter metadata, canonical links, `robots.txt`, and an XML sitemap.
- Clear current product architecture: server-first connections, data workspace, local operations, and backup/recovery.
- A browser demo that lets visitors try the interaction model without installing or signing up.
- Specific feature, backup, guide, comparison, and open-source pages rather than one generic landing page.
- Accurate `SoftwareApplication`, `SoftwareSourceCode`, `WebSite`, FAQ, Article, and breadcrumb JSON-LD where relevant.
- Current release/install links and honest support boundaries for every database engine.
- A public MIT repository, CI, release artifacts, APT publishing, cross-platform binaries, and detailed operator documentation.
- Explicit creator identity: Shreyam Adhikari / `@shreyam1008` in the README, visible footer, metadata, and structured data.
- A custom 404 page that returns users to current documentation.

### What is missing or underdeveloped

- A product subdomain and completed canonical migration.
- Search Console/Bing Webmaster measurement for the final domain, sitemap submission, and a recrawl workflow after important releases.
- A stable acquisition baseline: non-branded impressions, clicks, referral sources, release downloads, and conversion proxies are not recorded in one place.
- A current GitHub social-preview image and a short demo GIF that show the complete current product rather than an older workspace.
- High-intent, problem-solving guides for individual workflows. The site explains features well, but has little content matching searches such as “backup remote PostgreSQL to local folder” or “terminal MySQL client list all databases.”
- Earned listings in TUI/database directories and authentic community launch posts.
- A dedicated dbterm case-study page on `shreyam1008.com.np`; the existing project link helps, but a unique project story would strengthen both discovery and authorship.
- Testimonials or attributable user quotes. Do not manufacture ratings or reviews for structured data.

The old search result—“dbterm — Fast, Keyboard-First Terminal Database Client” with the earlier three-engine backup description—is a stale crawl. The live homepage now uses “dbterm — Connect, Query, Operate & Back Up Databases” and a current description. Requesting a recrawl can help discovery, but Google chooses when and how to refresh title links and snippets.

## Positioning that should remain consistent

### Category

**Open-source terminal database workbench and backup agent.**

“SQL client” is still a useful discovery phrase, but it is no longer the whole product category. “Database management platform” is too broad. “Backup tool” alone hides the daily data workspace.

### Primary message

> Connect once. See every database. Query, operate, and protect local or cloud data.

### One-sentence description

> dbterm is a keyboard-first, single-binary database workbench for PostgreSQL, MySQL/MariaDB, SQLite, Turso, and Cloudflare D1, with server-wide database discovery, typed data workflows, local service controls, and verified local or remote backups.

### The differentiators to repeat

1. PostgreSQL/MySQL connections can start at server scope; the database name is optional.
2. One focused keyboard workflow covers objects, queries, typed filters, relationship navigation, import, and streamed export.
3. Local and remote sources can back up to local/mounted or rclone storage in all four combinations.
4. A small native OS agent keeps scheduled jobs running after the TUI closes.
5. Backup artifacts are staged privately, verified, optionally compressed/encrypted, hashed, retained conservatively, inspected by content, and restored through explicit guards.

### Claims not to make

- Do not say “fastest,” “lightest,” “most secure,” “production-proof,” or “best DBeaver alternative” without a repeatable benchmark or external evidence.
- Do not imply that Turso or D1 restore is supported; their artifacts are inspectable, while restore currently targets PostgreSQL, MySQL/MariaDB, and local SQLite.
- Do not call the local/cloud backup matrix “zero configuration”; official database clients and rclone may be required.
- Do not say credentials are encrypted at rest. They are stored in private per-user files/catalog state and redacted from output.
- Do not present a synthetic comparison score or misrepresent competitors.

## Domain migration runbook

This is a controlled migration, not a second deployment.

### Before the switch

1. In GitHub account settings, verify `shreyam1008.com.np` for Pages using the exact TXT record GitHub provides. Keep the TXT record permanently to reduce takeover risk.
2. Do not use a wildcard DNS record. Add only the explicit product subdomain.
3. In Cloudflare DNS, add:

   ```text
   Type: CNAME
   Name: dbterm
   Target: shreyam1008.github.io
   Proxy: DNS only during certificate setup
   ```

   The target must not include `/dbterm`.

4. Update the website build configuration in one commit:

   ```text
   PUBLIC_SITE_URL=https://dbterm.shreyam1008.com.np/
   PUBLIC_BASE_PATH=/
   ```

5. Build locally and verify `/`, `/features/`, `/backup/`, `/guide/`, `/compare/`, `/open-source/`, `/404.html`, `robots.txt`, and `sitemap.xml`. Check canonical, Open Graph, structured-data, manifest, asset, and install links.
6. Add `dbterm.shreyam1008.com.np` as the repository’s Pages custom domain, wait for DNS validation, then enable HTTPS.
7. Publish and test both hostnames. Every old path must permanently redirect to its equivalent new path, not only the homepage.

### After the switch

1. Set the GitHub repository website to `https://dbterm.shreyam1008.com.np/`.
2. Update absolute URLs in JSON-LD, README documentation links, package metadata, personal-site project links, directory listings, and social profiles.
3. Verify both the old URL-prefix property and the new domain property in Google Search Console. Submit the new sitemap. Use Change of Address only if Search Console offers it for these properties; otherwise rely on permanent redirects, self-canonicals, internal links, and the new sitemap.
4. Add/import the new domain in Bing Webmaster Tools and submit the sitemap. Consider IndexNow only after the canonical domain is stable.
5. Request indexing for the homepage, Features, Backup Center, and the first high-intent guide. Do not repeatedly request indexing.
6. Keep the old-to-new redirects for at least one year and preferably indefinitely. Monitor redirect errors, canonical selection, indexed-page count, and backlinks to old URLs.
7. Keep the Cloudflare record DNS-only unless there is a concrete need for proxy features. If proxying later, recheck TLS, cache invalidation, status codes, security headers, and GitHub Pages domain validation first.

### Rollback

If HTTPS, path assets, or redirects fail, remove the repository custom domain, restore the GitHub Pages build environment to the `/dbterm` base, republish, and only then remove the DNS record. Never leave a dangling DNS record pointing at Pages.

## Technical search plan

### Ship and maintain

- One concise, descriptive title and one unique meta description per public page.
- One visible H1 that agrees with each page’s subject.
- Self-referencing canonicals and only canonical URLs in the sitemap.
- Crawlable internal links between homepage, Features, Backup Center, Guide, comparison, and new guides.
- Descriptive anchors such as “PostgreSQL backup guide,” not repeated “click here” text.
- `SoftwareApplication`/`SoftwareSourceCode` data with the actual creator, operating systems, license, current version, download URL, and price `0`.
- Breadcrumb JSON-LD for new guide/article pages.
- Release/guide Article JSON-LD only when the visible page contains the matching author and date.
- A current 1280×640 social card and a sharp product screenshot near relevant explanatory text.
- Good Core Web Vitals targets at the 75th percentile: LCP ≤2.5s, INP <200ms, CLS <0.1.

### Validate after every material site change

- Astro type check and production build.
- Internal links and HTTP status codes.
- Mobile and desktop screenshots.
- Google Rich Results Test and Schema Markup Validator.
- Search Console URL Inspection for rendered HTML, canonical, and resources.
- Lighthouse accessibility, performance, SEO, and best-practice checks; treat scores as diagnostics, not marketing claims.
- `robots.txt` sitemap URL and all sitemap URLs.

### Do not spend time on

- Meta keywords. Google does not use them.
- Repeating keywords unnaturally in headings, footers, alt text, or the domain.
- An `llms.txt` file as an SEO tactic. It can be considered later as an agent-facing convenience, but it has no established Google ranking benefit.
- Fake review/rating markup. Google’s SoftwareApplication rich result requires a real review or aggregate rating in addition to the offer; no rating is better than an invented one.
- Hundreds of generated “X alternative” or per-city/per-engine pages with substantially duplicated text.
- Submitting unchanged URLs to indexing services on every commit.

## Content that can earn search traffic

Publish original, tested workflow guides. Each should contain real commands, supported versions, prerequisites, screenshots, failure modes, security limits, and a link to the exact implementation/documentation. Do not turn these into thin doorway pages.

| Priority | Proposed URL | Search intent and angle |
| --- | --- | --- |
| P0 | `/guides/remote-postgresql-backup-local-rclone/` | Back up reachable PostgreSQL to a local folder or rclone remote; explain agent PATH, `pg_dump`, encryption, inspection, and restore drill. |
| P0 | `/guides/mysql-list-databases-without-default/` | Connect to MySQL without knowing a database name, browse account-visible databases, choose an optional default, and avoid sudo/database-password confusion. |
| P0 | `/guides/sqlite-backup-and-restore/` | Consistent snapshot, private staging, pre-restore copy, snapshot vs SQL restore, and filesystem safeguards. |
| P1 | `/guides/database-backup-agent-systemd-launchd-windows/` | Explain desktop/user vs server/system scheduling and when each scope is correct. |
| P1 | `/guides/encrypted-database-backups-age-rclone/` | End-to-end threat model, X25519 recipient/key custody, compression order, off-site storage, and recovery test. |
| P1 | `/guides/terminal-sql-client-workflow/` | Table pins, object palette, typed filters, foreign-key navigation, and streamed export as one practical walkthrough. |
| P2 | `/guides/cloudflare-d1-turso-backups/` | Clearly separate D1 native export and Turso transaction-backed logical export, limitations, and inspect-only restore status. |
| P2 | `/releases/` | Human-readable release notes with visible dates, changed workflows, upgrade notes, and links to GitHub artifacts. |

Keep the existing comparison page factual and sourced. Update the review date only when competitor facts are actually rechecked.

## Identity and association with Shreyam

Use a consistent relationship, not repetitive name stuffing:

- Product footer and README: “Created and maintained by Shreyam Adhikari · @shreyam1008.”
- JSON-LD: dbterm `author`, `creator`, and `maintainer` point to the same `Person` URL; use `sameAs` for the real GitHub profile.
- GitHub profile: pin dbterm and keep the repository About text, homepage, topics, and social preview current.
- Personal site: add a dedicated `/projects/dbterm` case study with the problem, design decisions, current capabilities, safety model, screenshots, and canonical link to dbterm. This page must be unique, not a copy of the dbterm homepage.
- dbterm site: link back to the personal site once in the footer/open-source page. More repeated links do not add more identity.
- Social profiles: use the same author spelling, handle, product URL, and icon.

## Earned backlinks and distribution

Backlinks are useful when they are editorially deserved and send relevant people. They are harmful or wasted when bought, automated, exchanged at scale, or dropped into unrelated threads.

### Highest-fit placements

1. **GitHub discovery** — accurate description, homepage, social preview, and topics: `terminal`, `tui`, `database`, `sql`, `postgresql`, `mysql`, `sqlite`, `turso`, `cloudflare-d1`, `database-client`, `database-backup`, `backup`, `golang`, `cross-platform`, `developer-tools`.
2. **Awesome TUIs** — open a small, standards-compliant PR placing dbterm next to the database clients, with one neutral sentence and no superlatives.
3. **Terminal Trove** — submit after producing a current PNG and short GIF. It explicitly prefers cross-platform standalone binaries and requires a preview.
4. **AlternativeTo** — suggest dbterm, then accurately relate it to DBeaver, Harlequin, rainfrog, LazySQL, and similar tools without claiming parity with desktop features.
5. **Package ecosystems** — keep APT, Debian, Homebrew, Scoop, WinGet, AUR, and `pkg.go.dev` metadata consistent; these pages become durable discovery/referral surfaces when packages are accepted.
6. **Personal project case study** — the strongest controlled backlink and authorship source because it adds original context.
7. **Release coverage** — ask maintainers/newsletters that already cover terminal tools to evaluate the actual product. Provide a demo and facts; do not ask only for a link.

### Community launch order

1. `r/tui`: product/workflow post with a current recording and a specific design question.
2. `r/golang`: share in the recurring “Small Projects” thread first; discuss architecture, cross-platform agents, cancellation, or TUI testing rather than dropping only a link.
3. Show HN: launch the runnable product, not the documentation page. Be present to answer questions for several hours.
4. `r/commandline`: only after reading the current rules; recent tool posts are frequently removed. Use the required flair/format and do not duplicate the `r/tui` text.
5. `r/selfhosted`: only if the post is specifically about owning backup artifacts and running the native agent without a hosted control plane. Avoid presenting dbterm as a web-based self-hosted service.
6. Product Hunt: lower priority than HN/TUI communities for this audience. Use it after the GIF, social card, short demo, and first user quotes exist.
7. DEV Community/Hashnode/personal log: publish the technical story behind server-first connections or the verified backup pipeline; link naturally to documentation.

Reddit rules are community-specific. Disclose that you made dbterm, contribute outside your own links, do not mass-cross-post the same copy, do not send unsolicited messages, and never ask for upvotes. Reddit’s site-wide spam policy explicitly warns against repetitive exposure and accounts whose contributions are primarily links to their own business/project.

## Ready-to-use launch copy

Treat these as editable drafts. Tailor each post to the community and answer questions honestly.

### GitHub About description

> Keyboard-first terminal database workbench for PostgreSQL, MySQL/MariaDB, SQLite, Turso, and D1—with server-wide discovery, typed data workflows, local services, and verified local/remote backups.

### Show HN

Title:

> Show HN: dbterm – a terminal database workbench with a native backup agent

Body:

> I built dbterm because I wanted the database loop to stay in one terminal: save a PostgreSQL/MySQL server login, browse every database that account can access, query and inspect data, follow foreign keys, filter/export rows, and then protect the same local or remote source without introducing a hosted control plane.
>
> The part that grew beyond a normal SQL TUI is Backup Center. It uses native engine dumps, private staging, verification, compression, optional age encryption, SHA-256 history, conservative retention, SMTP alerts, and systemd/launchd/Task Scheduler agents. Sources and destinations can each be local or remote. Restore is content-inspected and currently targets PostgreSQL, MySQL/MariaDB, and local SQLite.
>
> It is MIT-licensed, ships as one binary for Linux/macOS/Windows on amd64/arm64, and the website has a real in-browser SQLite demo. I would especially value feedback on the server-first connection model and the backup/restore safety boundaries.

### `r/tui`

Title:

> I built dbterm: one TUI for server-wide DB browsing, typed data work, and scheduled backups

Body outline:

1. Lead with the short GIF, not a wall of text.
2. Explain the problem: a saved MySQL/PostgreSQL login should not require remembering a database name.
3. Show the workflow: browse databases → pin a table → typed filter/FK jump → instant or scheduled backup.
4. State the five engines and the narrower three-engine restore scope.
5. Ask one real question: “Does Space-to-pin feel discoverable in the Tables context, or would you expect another key?”
6. Link to GitHub first and the feature map second.

### `r/golang` Small Projects comment

> I have been building dbterm, a cross-platform database TUI and backup agent in Go. The interesting engineering work recently has been keeping foreground queries/imports/exports cancellable without stale results winning races, and sharing one transactional backup catalog between the TUI, CLI, and OS-managed agent. Backups are serialized with leases and publish verified artifacts without clobbering existing files. It supports PostgreSQL, MySQL/MariaDB, SQLite, Turso, and D1; restore currently targets PostgreSQL, MySQL/MariaDB, and local SQLite. Repo: https://github.com/shreyam1008/dbterm — I would welcome feedback on the package boundaries and agent model.

### Terminal Trove fields

Tagline:

> A keyboard-first database workbench and backup agent for five SQL targets.

Description:

> Browse every database behind a saved server login, run and inspect SQL, follow foreign keys, filter and export rows, operate local services, and schedule verified local or remote backups from one cross-platform TUI.

Standout features:

> Server-first PostgreSQL/MySQL discovery; typed filters and foreign-key navigation; OS-managed backup jobs with compression, age encryption, retention, inspection, alerts, and guarded restore.

Who it is for:

> Terminal-first developers and small operators who want daily SQL exploration and owned database backups without a heavyweight desktop client or hosted backup control plane.

### Product Hunt

Name: `dbterm`  
Tagline: `Connect, query, operate, and protect databases from one terminal.`

Maker comment opening:

> I built dbterm to make a saved database connection useful beyond one database and one query screen. It now combines server-wide discovery, a typed keyboard data workflow, local service operations, and an OS-managed backup pipeline in a single open-source binary.

### Personal/LinkedIn post

> dbterm has grown from a focused SQL TUI into a complete terminal database workbench. Save a PostgreSQL or MySQL server login once, browse every accessible database, query and inspect data, and route local or remote backups to local/mounted or rclone storage. The native agent keeps scheduled jobs running after the TUI closes, with verification, compression, age encryption, retention, alerts, inspection, and guarded restore. v0.6.4 is open source and available for Linux, macOS, and Windows: https://github.com/shreyam1008/dbterm

## Assets to prepare before the public launch

1. A 15–25 second GIF at readable terminal scale:
   - select saved server;
   - browse accessible databases;
   - open one without creating another login;
   - type to find and Space-pin a table;
   - apply a typed filter or follow a foreign key;
   - open instant backup / Backup Center;
   - end on verified artifact history.
2. A 1280×640 social image with the dbterm mark, primary message, supported engines, and a real current UI crop. Keep essential text within the central safe area.
3. Three focused screenshots: server/database browser, data workflow, backup progress/history.
4. One simple architecture diagram: source → private native dump → verify → compress/encrypt → hash/publish → retain/notify → inspect/restore.
5. A two-minute narrated demo for YouTube and the personal project page. Show limitations as well as the happy path.

Do not use old screenshots anywhere in the launch materials. Do not hide important behavior behind a stylized mockup; at least one asset should show the real terminal.

## Measurement without invasive telemetry

dbterm should not add CLI usage telemetry merely for marketing. Measure the public surfaces:

### Weekly scorecard

| Metric | Source | Why it matters |
| --- | --- | --- |
| Non-branded impressions, clicks, CTR, average position | Google Search Console | Shows whether problem/feature pages are being discovered. |
| Indexed canonical URLs and crawl errors | Search Console + Bing Webmaster Tools | Catches migration and technical failures. |
| Landing pages and referring sites | Privacy-respecting web analytics or Cloudflare Web Analytics | Separates search, directory, community, and personal-site traffic. |
| Release asset downloads by OS/architecture | GitHub Releases API | Better adoption signal than page views alone; account for updater downloads. |
| Stars, forks, watchers, issues, first-time contributors | GitHub | Community interest and product feedback, not vanity ranking. |
| Install-copy clicks, GitHub clicks, guide→release clicks | Website events | Measures whether documentation helps users move forward. |
| Backup/connection issue themes | GitHub issues/discussions | Guides product documentation and future content. |

Record a baseline before a launch and review 7, 30, and 90 days later. Use campaign parameters only for distinct campaigns; canonical URLs should remain clean.

### Initial targets (directional, not promises)

- 100% of intended pages indexed on the canonical domain with no duplicate-host canonicals.
- First 10 relevant referring domains earned through packages, directories, personal writing, and editorial mentions—not bought links.
- Three high-intent guides published and updated against the current release.
- At least five substantive user conversations/issues that reveal workflow needs.
- Improving non-branded search impressions month over month; do not optimize solely for stars.

## 30 / 60 / 90 day execution

### Days 0–7: foundation

- [x] Rewrite homepage and README around the full current product.
- [x] Add Features, backup routing/pipeline, complete sitemap, current metadata, creator identity, and custom 404.
- [x] Update GitHub description and topics.
- [ ] Verify the current GitHub Pages sitemap in Google Search Console and Bing Webmaster Tools; request recrawl of the four key pages.
- [ ] Capture baseline search/referral/release metrics.
- [ ] Create the current GIF, social image, and three screenshots.
- [x] Verify `shreyam1008.com.np` in GitHub Pages account settings and prepare the DNS record.

### Days 8–30: canonical domain and launch

- [x] Execute the custom-domain runbook in one controlled window.
- [x] Add the personal `/projects/dbterm` case study and update the GitHub repository homepage.
- [x] Submit to Awesome TUI and email the Terminal Trove curator.
- [ ] Publish the remote PostgreSQL backup and server-first MySQL guides.
- [x] Publish tailored project posts to `r/tui`, `r/golang`, and `r/selfhosted`; monitor moderator review and respond to serious questions.
- [ ] Run Show HN only when the demo assets and first-run docs are polished.

### Days 31–60: proof and useful content

- [ ] Publish SQLite restore and native-agent guides.
- [x] Submit AlternativeTo with accurate product scope, platforms, license, source, and media.
- [ ] Turn repeated support questions into FAQ/guide improvements.
- [ ] Add real, attributable user quotes only with permission.
- [ ] Check canonical migration, redirects, coverage, Core Web Vitals, and referral quality.

### Days 61–90: repeatable release marketing

- [ ] Establish a release-note template: problem, visible change, upgrade note, safety boundary, screenshot, install link.
- [ ] Publish one technically substantial story per meaningful release, not a promotional post for every patch.
- [ ] Pitch terminal/database newsletters using a demo and unique technical angle.
- [ ] Decide on Product Hunt based on asset quality and actual user proof.
- [ ] Review which channels produced downloads, feedback, and contributors; stop the ones producing only low-quality clicks.

## Non-negotiable “do not” list

- Do not buy backlinks, stars, votes, reviews, directory packages, or “guest post” links that pass ranking credit.
- Do not automate comments, DMs, Reddit posts, directory submissions, or link exchanges.
- Do not ask friends to upvote Show HN, Reddit, or Product Hunt.
- Do not post the same launch copy to many communities on the same day.
- Do not invent users, testimonials, ratings, download counts, benchmarks, or security claims.
- Do not create a second indexed copy of the site.
- Do not switch canonical domains before DNS, HTTPS, base paths, redirects, Search Console, and rollback are ready.
- Do not make marketing copy broader than the actual engine/restore support.
- Do not let release marketing outrun support capacity; be present where the product is posted.

## Research sources

- [Google SEO Starter Guide](https://developers.google.com/search/docs/fundamentals/seo-starter-guide)
- [Google title-link guidance](https://developers.google.com/search/docs/appearance/title-link)
- [Google snippet/meta-description guidance](https://developers.google.com/search/docs/appearance/snippet)
- [Google sitemap guidance](https://developers.google.com/search/docs/crawling-indexing/sitemaps/build-sitemap)
- [Google site-move guidance](https://developers.google.com/search/docs/crawling-indexing/site-move-with-url-changes)
- [Google SoftwareApplication structured data](https://developers.google.com/search/docs/appearance/structured-data/software-app)
- [Google Core Web Vitals](https://developers.google.com/search/docs/appearance/core-web-vitals)
- [Google spam policies](https://developers.google.com/search/docs/essentials/spam-policies)
- [GitHub Pages custom-domain guidance](https://docs.github.com/en/pages/configuring-a-custom-domain-for-your-github-pages-site/managing-a-custom-domain-for-your-github-pages-site)
- [GitHub Pages domain verification](https://docs.github.com/en/pages/configuring-a-custom-domain-for-your-github-pages-site/verifying-your-custom-domain-for-github-pages)
- [GitHub repository topics](https://docs.github.com/en/repositories/managing-your-repositorys-settings-and-features/customizing-your-repository/classifying-your-repository-with-topics)
- [GitHub repository social preview](https://docs.github.com/en/repositories/managing-your-repositorys-settings-and-features/customizing-your-repository/customizing-your-repositorys-social-media-preview)
- [Bing Webmaster Tools](https://www.bing.com/webmasters/help)
- [Bing IndexNow](https://www.bing.com/webmasters/help/indexnow-0z209wby)
- [Reddit spam policy](https://support.reddithelp.com/hc/en-us/articles/360043504051-Spam)
- [Show HN guidelines](https://news.ycombinator.com/showhn.html)
- [Product Hunt launch guide](https://www.producthunt.com/launch)
- [Terminal Trove submission criteria](https://terminaltrove.com/post/)
- [Awesome TUIs](https://github.com/rothgar/awesome-tuis)
- [AlternativeTo submission FAQ](https://alternativeto.net/faq/#add-a-new-application)
