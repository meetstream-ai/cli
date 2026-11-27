import { readFileSync, existsSync } from "node:fs";
import { spawnSync } from "node:child_process";

// The API URL from your shell, before .env is read: the CLI keeps using it, so a
// project file cannot send your sign-in to another host.
const SHELL_API_URL = process.env.MEETSTREAM_API_URL;

// Read .env next to this file (no packages needed).
const envFile = new URL("./.env", import.meta.url);
if (existsSync(envFile)) {
  for (const line of readFileSync(envFile, "utf8").split("\n")) {
    const i = line.indexOf("=");
    if (i > 0 && !line.trimStart().startsWith("#")) {
      const k = line.slice(0, i).trim();
      if (!(k in process.env)) process.env[k] = line.slice(i + 1).trim();
    }
  }
}

export const API = (process.env.MEETSTREAM_API_URL || "https://api.meetstream.ai/api/v1").replace(/\/$/, "");
const KEY = process.env.MEETSTREAM_API_KEY || "";
export const POLL_MS = /127\.0\.0\.1|localhost/.test(KEY ? API : SHELL_API_URL || "") ? 1000 : 5000;

// No key in .env (your laptop): the meetstream CLI makes the call with your sign-in.
function callViaCli(method, path, body) {
  const env = { ...process.env };
  if (SHELL_API_URL) env.MEETSTREAM_API_URL = SHELL_API_URL;
  else delete env.MEETSTREAM_API_URL;
  const args = ["api", path, "-X", method, "--envelope", "--no-input"];
  if (body !== undefined) args.push("-d", "-");
  const r = spawnSync("meetstream", args, { input: body === undefined ? undefined : JSON.stringify(body), env, encoding: "utf8", timeout: 60000 });
  if (r.error) throw new Error("no MeetStream key in .env and the meetstream CLI is not installed (install it, or run: meetstream env --with-key)");
  if (r.status !== 0) throw new Error(`meetstream api failed: ${(r.stdout || r.stderr).trim().slice(0, 300)} (signed in? run: meetstream login)`);
  const out = JSON.parse(r.stdout);
  return [out.status, out.body];
}
export const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

// Check an id from a webhook or an API answer before it goes into a URL path.
export function safeId(value) {
  if (typeof value !== "string" || !/^[A-Za-z0-9_-]{1,128}$/.test(value)) throw new Error(`unexpected id: ${value}`);
  return encodeURIComponent(value);
}

// With MEETSTREAM_API_KEY in .env (a server) this calls the API directly;
// without it (your laptop) it goes through the CLI and your sign-in.
export async function call(method, path, body) {
  if (!KEY) return callViaCli(method, path, body);
  const res = await fetch(API + path, {
    method,
    headers: { Authorization: `Token ${KEY}`, "Content-Type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const text = await res.text();
  let json = null;
  try { json = text ? JSON.parse(text) : null; } catch { json = { detail: text }; }
  return [res.status, json];
}

// The transcript id is not in any webhook: read it from the bot detail, then poll.
export async function fetchTranscript(botId, timeoutMs = 900000) {
  botId = safeId(botId);
  const end = Date.now() + timeoutMs;
  while (Date.now() < end) {
    const [, detail] = await call("GET", `/bots/${botId}/detail`);
    const tid = detail?.bot_details?.transcript_id;
    if (tid) {
      const [code, body] = await call("GET", `/transcript/${safeId(tid)}/get_transcript?raw=false`);
      if (code === 200 && body) return Array.isArray(body) ? body : body.message ?? body;
      if (code === 422) throw new Error(body?.message || "transcript failed");
    }
    await sleep(POLL_MS);
  }
  throw new Error("transcript not ready in time");
}

// Segment text: words[].text, then punctuated_word, then word, then the flat transcript or text.
export const segmentText = (s = {}) => {
  const words = (s.words || [])
    .map((w) => w?.text || w?.punctuated_word || w?.word || "")
    .filter(Boolean)
    .join(" ");
  return words || s.transcript || s.text || "";
};

export const transcriptLines = (segments = []) =>
  segments
    .map((s) => `${s.participant?.name || s.speaker || "Unknown"}: ${segmentText(s)}`)
    .filter((l) => !l.endsWith(": "));
