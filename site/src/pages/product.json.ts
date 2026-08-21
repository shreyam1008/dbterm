import type { APIRoute } from "astro";
import product from "../../../product.json?raw";
import { staticDocument } from "../lib/static-document";

export const prerender = true;

export const GET: APIRoute = () => staticDocument(product, "application/json");
