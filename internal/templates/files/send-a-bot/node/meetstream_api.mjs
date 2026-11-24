import { readFileSync, existsSync } from "node:fs";

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
export const POLL_MS = /127\.0\.0\.1|localhost/.test(API) ? 1000 : 5000;
export const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

export async function call(method, path, body) {
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
  const end = Date.now() + timeoutMs;
  while (Date.now() < end) {
    const [, detail] = await call("GET", `/bots/${botId}/detail`);
    const tid = detail?.bot_details?.transcript_id;
    if (tid) {
      const [code, body] = await call("GET", `/transcript/${tid}/get_transcript?raw=false`);
      if (code === 200 && body) return Array.isArray(body) ? body : body.message ?? body;
      if (code === 422) throw new Error(body?.message || "transcript failed");
    }
    await sleep(POLL_MS);
  }
  throw new Error("transcript not ready in time");
}

export const transcriptLines = (segments = []) =>
  segments
    .map((s) => `${s.participant?.name || s.speaker || "Unknown"}: ${s.transcript || s.text || ""}`)
    .filter((l) => !l.endsWith(": "));
