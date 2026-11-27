// Send a MeetStream bot to a meeting, follow it, print the transcript.
// Usage: node send_bot.mjs <meeting-url>      (Node 18 or later, no packages)
import { call, fetchTranscript, transcriptLines, sleep, POLL_MS, safeId } from "./meetstream_api.mjs";

const PROVIDERS = {
  meetstream: { meetstream: { language: "auto", translate: false } },
  deepgram: { deepgram: { model: "nova-3", language: "en", diarize: true } },
  assemblyai: { assemblyai: { speech_models: ["best"], language_code: "en_us", speaker_labels: true } },
};
const PROVIDER = process.env.TRANSCRIPTION_PROVIDER || "[[.Transcription]]";
const TERMINAL = ["stopped", "done", "notallowed", "denied", "failed", "error"];

const link = process.argv[2];
if (!link) { console.error("usage: node send_bot.mjs <meeting-url>"); process.exit(2); }

const [code, bot] = await call("POST", "/bots/create_bot", {
  meeting_link: link,
  bot_name: "[[.BotName]]",
  recording_config: { transcript: { provider: PROVIDERS[PROVIDER] ?? { [PROVIDER]: {} } } },
});
if (code >= 300) { console.error(`create_bot failed (${code})`, bot); process.exit(1); }
const botId = safeId(bot.bot_id);
console.log(`bot ${botId} sent`);

let last = "";
for (;;) {
  const [, st] = await call("GET", `/bots/${botId}/status`);
  const status = st?.status || "";
  if (status !== last) { console.log(`  ${new Date().toLocaleTimeString()}  ${status}`); last = status; }
  if (TERMINAL.includes(status.toLowerCase())) break;
  await sleep(POLL_MS);
}
if (!["stopped", "done"].includes(last.toLowerCase())) { console.error(`bot ended without recording: ${last}`); process.exit(1); }
console.log("waiting for the transcript ...");
for (const line of transcriptLines(await fetchTranscript(botId))) console.log("  " + line);
