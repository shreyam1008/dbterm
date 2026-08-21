import type { APIRoute } from "astro";
import mcp from "../../../docs/mcp.md?raw";
import { staticDocument } from "../lib/static-document";

export const prerender = true;

export const GET: APIRoute = () => staticDocument(mcp, "text/markdown");
