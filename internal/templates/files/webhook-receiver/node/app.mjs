// Receive every MeetStream bot event and branch on it (Node 18+, no packages).
// Run: node app.mjs   then: meetstream listen --forward-to http://127.0.0.1:8000/webhook
import { createServer } from "node:http";

const PORT = Number(process.env.PORT || 8000);
const seen = new Set();
const ENDINGS = {
  "bot.stopped": "clean exit", "bot.kicked": "removed by a participant",
  "bot.notallowed": "not admitted (waiting room timeout)", "bot.denied": "the host refused entry", "bot.failed": "unexpected error",
};

function handle(e) {
  if (!e.event && e.transcript) return console.log(`[live] ${e.speakerName || "Unknown"}: ${e.transcript}`);
  if (e.event === "bot.stopped") return console.log(`[${e.bot_id}] ended: ${ENDINGS[e.bot_event] || e.bot_event}`);
  if (e.event === "transcription.processed") return console.log(`[${e.bot_id}] transcript ready`);
  if (e.event === "transcription.failed") return console.log(`[${e.bot_id}] transcript failed: ${e.message}`);
  console.log(`[${e.bot_id}] ${e.event}`);
}

createServer((req, res) => {
  if (req.method !== "POST" || req.url !== "/webhook") { res.writeHead(404).end(); return; }
  let raw = "";
  req.on("data", (c) => (raw += c));
  req.on("end", () => {
    res.writeHead(200, { "Content-Type": "application/json" }).end('{"status":"ok"}'); // 2xx fast
    let e;
    try { e = JSON.parse(raw); } catch { return; }
    const key = [e.bot_id, e.event, e.bot_event, e.timestamp, e.turn_order].join("|");
    if (seen.has(key)) return; // MeetStream can retry
    seen.add(key);
    setImmediate(() => handle(e));
  });
}).listen(PORT, "127.0.0.1", () => console.log(`listening on http://127.0.0.1:${PORT}/webhook`));
