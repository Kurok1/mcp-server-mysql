#!/usr/bin/env node
/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since 1.2.1
 */
import { existsSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const source = path.join(root, "dist", "client", "index.html");
const destination = path.resolve(root, "..", "..", "internal", "ui", "query-results.html");

if (!existsSync(source)) throw new Error(`Missing MCP App bundle: ${source}`);
mkdirSync(path.dirname(destination), { recursive: true });
const bundle = readFileSync(source, "utf8").replace(/^[\t ]+$/gm, "");
writeFileSync(destination, bundle);
console.log(`Prepared embedded MCP App: ${destination}`);
