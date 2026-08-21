import type { APIRoute } from "astro";
import changeProfiler from "../../../docs/change-profiler.md?raw";
import { staticDocument } from "../lib/static-document";

export const prerender = true;

export const GET: APIRoute = () => staticDocument(changeProfiler, "text/markdown");
