import type { APIRoute } from "astro";
import guide from "../../../docs/user-guide.md?raw";
import { staticDocument } from "../lib/static-document";

export const prerender = true;

export const GET: APIRoute = () =>
  staticDocument(`# dbterm complete user guide\n\n${guide.trim()}\n`, "text/markdown");
