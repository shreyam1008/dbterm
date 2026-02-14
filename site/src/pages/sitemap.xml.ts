import type { APIRoute } from "astro";

export const prerender = true;

const routes = [
  { path: "", priority: "1.0", changefreq: "weekly" },
  { path: "guide/", priority: "0.9", changefreq: "weekly" },
  { path: "open-source/", priority: "0.8", changefreq: "monthly" }
];

export const GET: APIRoute = ({ site }) => {
  const root = site ? site.toString() : "https://example.github.io/dbterm/";
  const normalizedRoot = root.endsWith("/") ? root : `${root}/`;
  const updated = new Date().toISOString();

  const entries = routes
    .map(
      (route) => `  <url>
    <loc>${normalizedRoot}${route.path}</loc>
    <lastmod>${updated}</lastmod>
    <changefreq>${route.changefreq}</changefreq>
    <priority>${route.priority}</priority>
  </url>`
    )
    .join("\n");

  const xml = `<?xml version="1.0" encoding="UTF-8"?>
<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
${entries}
</urlset>`;

  return new Response(xml, {
    headers: {
      "Content-Type": "application/xml; charset=utf-8"
    }
  });
};
