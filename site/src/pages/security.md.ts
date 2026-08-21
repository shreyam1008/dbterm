import type { APIRoute } from "astro";
import security from "../../../SECURITY.md?raw";
import { staticDocument } from "../lib/static-document";

export const prerender = true;

export const GET: APIRoute = () => staticDocument(security, "text/markdown");
