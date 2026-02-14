import { defineConfig } from "astro/config";

const repositoryName = process.env.PUBLIC_REPOSITORY_NAME || "dbterm";
const basePath = process.env.PUBLIC_BASE_PATH || `/${repositoryName}`;
const siteUrl =
  process.env.PUBLIC_SITE_URL ||
  `https://${process.env.PUBLIC_GITHUB_OWNER || "shreyam1008"}.github.io/${repositoryName}/`;

export default defineConfig({
  output: "static",
  site: siteUrl,
  base: basePath,
  trailingSlash: "always"
});
