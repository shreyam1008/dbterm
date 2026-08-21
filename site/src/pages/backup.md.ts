import type { APIRoute } from "astro";
import backup from "../../../docs/backup.md?raw";
import { staticDocument } from "../lib/static-document";

export const prerender = true;

export const GET: APIRoute = () => staticDocument(backup, "text/markdown");
