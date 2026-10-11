// Drives MonoFlow's opencode plugin the way opencode does, with a stub client:
//   node opencode-drive.mjs <plugin.js> <worktree dir>
// 1. session.idle: the plugin runs `hook stop` and prompts the session with what needs action.
// 2. tool.execute.after: the plugin appends newly delivered events to the tool's output.
import { copyFileSync } from "node:fs";
import { join } from "node:path";
import { tmpdir } from "node:os";

const [, , pluginPath, directory] = process.argv;
const esm = join(tmpdir(), `monoflow-plugin-${process.pid}.mjs`);
copyFileSync(pluginPath, esm);
const { MonoflowGuard } = await import(esm);

let prompted = "";
const client = { session: { prompt: async (req) => { prompted += req.body.parts.map((p) => p.text).join(""); } } };
const plugin = await MonoflowGuard({ client, directory });

await plugin.event({ event: { type: "session.idle", properties: { sessionID: "s1" } } });
const deadline = Date.now() + 120000;
while (!prompted && Date.now() < deadline) await new Promise((r) => setTimeout(r, 500));
console.log("IDLE_PROMPT<<" + prompted + ">>");

const out = { output: "tool output" };
await plugin["tool.execute.after"]({ tool: "bash" }, out);
console.log("AFTER<<" + out.output + ">>");
const again = { output: "tool output" };
await plugin["tool.execute.after"]({ tool: "bash" }, again);
console.log("AFTER2<<" + again.output + ">>");
