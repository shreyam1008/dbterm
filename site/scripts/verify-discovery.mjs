import { createHash } from "node:crypto";
import { readFile, readdir } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";

const siteDir = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const repoDir = path.resolve(siteDir, "..");
const distDir = path.join(siteDir, "dist");
const productSource = await readFile(path.join(repoDir, "product.json"), "utf8");
const siteRoot = new URL(JSON.parse(productSource).canonicalUrl);
const routes = ["", "features/", "backup/", "guide/", "compare/", "open-source/", "agents/"];

const assert = (condition, message) => {
  if (!condition) throw new Error(message);
};

const read = (root, relativePath) => readFile(path.join(root, relativePath), "utf8");
const readDist = (relativePath) => read(distDir, relativePath);
const expectedUrl = (route) => new URL(route, siteRoot).href;
const htmlPath = (route) => (route ? `${route}index.html` : "index.html");
const outputPath = (url) => {
  assert(url.pathname.startsWith(siteRoot.pathname), `local URL is outside the canonical site root: ${url.href}`);
  const relativePath = decodeURIComponent(url.pathname.slice(siteRoot.pathname.length));
  return relativePath === "" ? "index.html" : relativePath.endsWith("/") ? `${relativePath}index.html` : relativePath;
};
const attribute = (tag, name) =>
  tag.match(new RegExp(`\\b${name}=["']([^"']*)["']`, "i"))?.[1];
const tags = (html, name) => html.match(new RegExp(`<${name}\\b[^>]*>`, "gi")) ?? [];
const metaContent = (html, name) =>
  tags(html, "meta")
    .filter((tag) => attribute(tag, "name")?.toLowerCase() === name.toLowerCase())
    .map((tag) => attribute(tag, "content") ?? "");
const linksByRel = (html, rel) =>
  tags(html, "link")
    .filter((tag) => (attribute(tag, "rel") ?? "").toLowerCase().split(/\s+/).includes(rel))
    .map((tag) => ({ href: attribute(tag, "href"), type: attribute(tag, "type") }));

const releaseManifest = await read(repoDir, "cmd/dbterm/releases.txt");
const releaseLine = releaseManifest.split(/\r?\n/).find((line) => line.trim() && !line.trimStart().startsWith("#"));
const [releaseVersion, releaseName] = releaseLine?.split("|").map((value) => value.trim()) ?? [];
assert(releaseVersion && releaseName, "release manifest has no current version and name");
const releaseLabel = `v${releaseVersion} · ${releaseName}`;
const releaseUrl = `https://github.com/shreyam1008/dbterm/releases/tag/v${releaseVersion}`;
const sitePackage = JSON.parse(await read(siteDir, "package.json"));
assert(sitePackage.version === releaseVersion, "site/package.json version differs from the release manifest");

const sitemap = await readDist("sitemap.xml");
const sitemapUrls = [...sitemap.matchAll(/<loc>([^<]+)<\/loc>/g)].map((match) => match[1]);
const expectedUrls = routes.map(expectedUrl);
const generatedPaths = await readdir(distDir, { recursive: true });
const generatedFiles = new Set(generatedPaths);
const generatedRoutes = generatedPaths
  .filter((relativePath) => relativePath === "index.html" || relativePath.endsWith("/index.html"))
  .map((relativePath) => (relativePath === "index.html" ? "" : relativePath.slice(0, -"index.html".length)))
  .sort();
assert(
  generatedRoutes.length === routes.length &&
    generatedRoutes.every((route) => routes.includes(route)) &&
    routes.every((route) => generatedRoutes.includes(route)),
  `generated HTML routes differ from the documented public route set:\n${generatedRoutes.join("\n")}`
);
assert(sitemapUrls.length === routes.length, `sitemap must contain exactly ${routes.length} URLs`);
assert(new Set(sitemapUrls).size === sitemapUrls.length, "sitemap contains a duplicate URL");
assert(
  expectedUrls.every((url) => sitemapUrls.includes(url)) && sitemapUrls.every((url) => expectedUrls.includes(url)),
  `sitemap routes differ from the public route set:\n${sitemapUrls.join("\n")}`
);

const titles = new Set();
const descriptions = new Set();
for (const route of routes) {
  const html = await readDist(htmlPath(route));
  const label = route || "/";
  const title = html.match(/<title>([\s\S]*?)<\/title>/i)?.[1].trim();
  const descriptionValues = metaContent(html, "description");
  const canonicals = linksByRel(html, "canonical");
  const robots = metaContent(html, "robots").join(",").toLowerCase();

  assert(title, `${label} has no non-empty title`);
  assert(descriptionValues.length === 1 && descriptionValues[0].trim(), `${label} needs one description`);
  assert((html.match(/<h1(?:\s|>)/gi) ?? []).length === 1, `${label} needs exactly one h1`);
  assert(canonicals.length === 1 && canonicals[0].href === expectedUrl(route), `${label} canonical is not self-referencing`);
  assert(!robots.includes("noindex"), `${label} is unexpectedly noindex`);
  assert(!titles.has(title), `${label} duplicates another page title`);
  assert(!descriptions.has(descriptionValues[0]), `${label} duplicates another page description`);
  titles.add(title);
  descriptions.add(descriptionValues[0]);
}

const notFound = await readDist("404.html");
assert(metaContent(notFound, "robots").some((value) => /(?:^|,)\s*noindex(?:,|$)/i.test(value)), "404 must be noindex");
assert(linksByRel(notFound, "canonical").length === 0, "404 must not publish a canonical URL");

const htmlDocuments = routes.map((route) => ({ path: htmlPath(route), url: expectedUrl(route) }));
htmlDocuments.push({ path: "404.html", url: expectedUrl("404.html") });
for (const page of htmlDocuments) {
  const html = await readDist(page.path);
  for (const anchor of tags(html, "a")) {
    const href = attribute(anchor, "href");
    if (!href) continue;
    const targetUrl = new URL(href, page.url);
    if (targetUrl.origin !== siteRoot.origin) continue;
    const targetPath = outputPath(targetUrl);
    assert(generatedFiles.has(targetPath), `${page.path} links to missing output: ${href}`);
    if (!targetUrl.hash || !targetPath.endsWith(".html")) continue;
    const fragment = decodeURIComponent(targetUrl.hash.slice(1));
    const targetHtml = await readDist(targetPath);
    const targets = new Set(
      [...targetHtml.matchAll(/\b(?:id|name)=["']([^"']+)["']/gi)].map((match) => match[1])
    );
    assert(targets.has(fragment), `${page.path} links to missing fragment: ${href}`);
  }
}

const guideHtml = await readDist("guide/index.html");
for (const marker of [
  "Create and manage connections",
  "Read-Only Guard",
  "not a database security boundary",
  "Work with result rows and columns",
  "Back up and restore databases",
  "Required tools and agent PATH",
  "Connect trusted AI agents through MCP",
  "Troubleshooting"
]) {
  assert(guideHtml.includes(marker), `guide HTML is missing: ${marker}`);
}
const guideAlternates = linksByRel(guideHtml, "alternate");
assert(
  guideAlternates.some((link) => link.type === "text/markdown" && link.href === expectedUrl("guide.md")),
  "guide must link its Markdown alternate"
);
assert(
  linksByRel(guideHtml, "describedby").some(
    (link) => link.href && new URL(link.href, expectedUrl("guide/")).href === expectedUrl("llms.txt")
  ),
  "guide must link the agent-readable documentation index"
);

const documentMarkers = new Map([
  ["guide.md", ["# dbterm complete user guide", "## Create and manage connections", "## Troubleshooting"]],
  ["backup.md", ["# dbterm Backup Center", "## Inspect and restore"]],
  ["agents.md", ["# dbterm MCP server", "## SQL safety"]],
  ["change-profiler.md", ["# Change Profiler architecture", "## Portable exact mode"]],
  ["readme.md", ["# dbterm", "## CLI reference"]],
  ["security.md", ["# Security policy"]],
  ["llms-full.txt", [
    "# dbterm — complete product and operating documentation",
    "# Complete user guide",
    "# dbterm Backup Center",
    "# dbterm MCP server",
    "# Change Profiler architecture",
    "# Security policy",
    "# Release manifest"
  ]],
  ["product.json", [
    "\"slug\": \"dbterm\"",
    "\"canonicalUrl\": \"https://dbterm.shreyam1008.com.np/\"",
    "\"documentation\"",
    "\"agentDiscovery\""
  ]]
]);
for (const [relativePath, markers] of documentMarkers) {
  const contents = await readDist(relativePath);
  assert(contents.trim(), `${relativePath} is empty`);
  for (const marker of markers) assert(contents.includes(marker), `${relativePath} is missing: ${marker}`);
}

const indexHtml = await readDist("index.html");
assert(indexHtml.includes(`"softwareVersion":"${releaseVersion}"`), "home structured data version differs from the release manifest");
assert(indexHtml.includes(releaseUrl), "home release URL differs from the release manifest");
assert(indexHtml.split(releaseLabel).length - 1 >= 3, "home release labels differ from the release manifest");
assert(guideHtml.includes(`"softwareVersion":"${releaseVersion}"`), "guide structured data version differs from the release manifest");
assert(guideHtml.includes(`Complete manual · dbterm ${releaseLabel}`), "guide visible release differs from the release manifest");
assert(guideHtml.includes(`v${releaseVersion} “${releaseName}”`), "guide manual release differs from the release manifest");
const fullDocumentation = await readDist("llms-full.txt");
assert(
  fullDocumentation.includes(`Canonical full-text context for dbterm v${releaseVersion} “${releaseName}”`),
  "llms-full release differs from the release manifest"
);

for (const relativePath of ["readme.md", "llms-full.txt"]) {
  const contents = await readDist(relativePath);
  assert(!/\]\((?:LICENSE|docs\/)/.test(contents), `${relativePath} retains a repository-relative README link`);
  for (const match of contents.matchAll(/\]\(([^)\s]+)\)/g)) {
    const targetUrl = new URL(match[1], expectedUrl(relativePath));
    if (targetUrl.origin !== siteRoot.origin) continue;
    assert(generatedFiles.has(outputPath(targetUrl)), `${relativePath} links to missing output: ${match[1]}`);
  }
}

const llms = await readDist("llms.txt");
const llmsListLines = llms.split(/\r?\n/).filter((line) => line.startsWith("- "));
assert(llmsListLines.length > 0, "llms.txt has no documentation links");
assert(
  llmsListLines.every((line) => /^- \[[^\]]+\]\((https?:\/\/[^)]+)\):\s+\S/.test(line)),
  "every llms.txt list item must use '- [label](URL): description' syntax"
);
assert(!/^- [^\[][^\n]*:\s*https?:\/\//m.test(llms), "llms.txt contains a bare-URL list item");

const llmsLinks = [...llms.matchAll(/\[[^\]]+\]\((https?:\/\/[^)]+)\)/g)].map((match) => new URL(match[1]));
for (const url of llmsLinks.filter((url) => url.origin === siteRoot.origin)) {
  const contents = await readDist(outputPath(url));
  assert(contents.trim(), `local llms.txt URL has no generated output: ${url.href}`);
}

const generatedProduct = await readDist("product.json");
assert(generatedProduct.trimEnd() === productSource.trimEnd(), "dist/product.json differs from repository product.json");
const product = JSON.parse(generatedProduct);
for (const channel of ["Product website", "GitHub Releases"]) {
  const distribution = product.distributions?.find((entry) => entry.channel === channel);
  assert(distribution?.version === releaseVersion, `${channel} version differs from the release manifest`);
}
assert(
  product.distributions?.find((entry) => entry.channel === "GitHub Releases")?.url === releaseUrl,
  "GitHub Releases URL differs from the release manifest"
);

const localSkill = await read(repoDir, ".agents/skills/use-dbterm/SKILL.md");
const publicSkill = await read(siteDir, "public/.well-known/agent-skills/use-dbterm/SKILL.md");
const generatedSkill = await readDist(".well-known/agent-skills/use-dbterm/SKILL.md");
assert(localSkill === publicSkill && localSkill === generatedSkill, "Agent Skill copies have drifted");
const skillDigest = `sha256:${createHash("sha256").update(localSkill).digest("hex")}`;
const publicIndex = await read(siteDir, "public/.well-known/agent-skills/index.json");
const generatedIndex = await readDist(".well-known/agent-skills/index.json");
assert(publicIndex === generatedIndex, "generated Agent Skill index differs from its public source");
const indexedSkill = JSON.parse(generatedIndex).skills?.find((skill) => skill.name === "use-dbterm");
assert(indexedSkill?.digest === skillDigest, "Agent Skill discovery digest does not match SKILL.md");

const robotsText = await readDist("robots.txt");
assert(/^User-agent:\s*\*$/im.test(robotsText), "robots.txt needs the wildcard user agent");
assert(/^Allow:\s*\/$/im.test(robotsText), "robots.txt must allow crawling");
assert(new RegExp(`^Sitemap:\\s*${expectedUrl("sitemap.xml").replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}$`, "im").test(robotsText), "robots.txt sitemap URL is wrong");
const contentSignal = robotsText.match(/^Content-Signal:\s*(.+)$/im)?.[1] ?? "";
for (const signal of ["search=yes", "ai-input=yes", "ai-train=yes", "use=full"]) {
  assert(contentSignal.split(/\s*,\s*/).includes(signal), `robots.txt Content-Signal is missing ${signal}`);
}

console.log(`Discovery verification passed: ${routes.length} indexable pages, ${documentMarkers.size} generated documents, Agent Skill ${skillDigest}.`);
