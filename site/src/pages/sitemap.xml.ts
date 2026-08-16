import type { APIRoute } from "astro";

export const prerender = true;

const routes = ["", "features/", "backup/", "guide/", "compare/", "open-source/", "agents/"];

export const GET: APIRoute = ({ site }) => {
  const root = site ? site.toString() : "https://example.github.io/dbterm/";
  const normalizedRoot = root.endsWith("/") ? root : `${root}/`;
  const entries = routes
    .map(
      (route) => `  <url>
    <loc>${normalizedRoot}${route}</loc>
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
