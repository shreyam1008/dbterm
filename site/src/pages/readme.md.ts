import type { APIRoute } from "astro";
import readme from "../../../README.md?raw";
import { rewriteReadmeLinksForSite, staticDocument } from "../lib/static-document";

export const prerender = true;

export const GET: APIRoute = () => staticDocument(rewriteReadmeLinksForSite(readme), "text/markdown");
