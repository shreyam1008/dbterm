import { defineConfig } from "astro/config";
import tailwindcss from "@tailwindcss/vite";

const basePath = process.env.PUBLIC_BASE_PATH || "/";
const siteUrl = process.env.PUBLIC_SITE_URL || "https://dbterm.shreyam1008.com.np/";

export default defineConfig({
  output: "static",
  site: siteUrl,
  base: basePath,
  trailingSlash: "always",
  vite: {
    plugins: [tailwindcss()],
    optimizeDeps: {
      exclude: ["@sqlite.org/sqlite-wasm"]
    }
  }
});
