import type { APIRoute } from "astro";
import readme from "../../../README.md?raw";
import guide from "../../../docs/user-guide.md?raw";
import backup from "../../../docs/backup.md?raw";
import mcp from "../../../docs/mcp.md?raw";
import changeProfiler from "../../../docs/change-profiler.md?raw";
import security from "../../../SECURITY.md?raw";
import releases from "../../../cmd/dbterm/releases.txt?raw";
import { currentRelease } from "../lib/release";
import { rewriteReadmeLinksForSite, staticDocument } from "../lib/static-document";

export const prerender = true;

const fullDocumentation = `# dbterm — complete product and operating documentation

> Canonical full-text context for dbterm v${currentRelease.version} “${currentRelease.name}”, generated from the repository's human manuals. dbterm is a keyboard-first local database workbench and native backup agent created by Shreyam Adhikari (shreyam1008).

Canonical website: https://dbterm.shreyam1008.com.np/
Source: https://github.com/shreyam1008/dbterm
License: MIT

---

${rewriteReadmeLinksForSite(readme).trim()}

---

# Complete user guide

${guide.trim()}

---

${backup.trim()}

---

${mcp.trim()}

---

${changeProfiler.trim()}

---

${security.trim()}

---

# Release manifest

\`\`\`text
${releases.trim()}
\`\`\`
`;

export const GET: APIRoute = () => staticDocument(fullDocumentation, "text/plain");
