import type { APIRoute } from "astro";

export const prerender = true;

export const GET: APIRoute = ({ site }) => {
  const root = site ? site.toString() : "https://example.github.io/dbterm/";
  const normalizedRoot = root.endsWith("/") ? root : `${root}/`;

  const robots = [
    "User-agent: *",
    "Allow: /",
    `Sitemap: ${normalizedRoot}sitemap.xml`
  ].join("\n");

  return new Response(robots, {
    headers: {
      "Content-Type": "text/plain; charset=utf-8"
    }
  });
};
