// Dev-only: verify the Dynatrace MCP server starts and completes an MCP handshake.
// Reads .env, spawns the server over stdio, does initialize + tools/list.
// Usage: node scripts/mcp_check.mjs
import { spawn } from "node:child_process";
import { readFileSync } from "node:fs";
import readline from "node:readline";

const env = { ...process.env };
for (const line of readFileSync(new URL("../.env", import.meta.url), "utf8").split(/\r?\n/)) {
  const m = line.match(/^([A-Z0-9_]+)=(.*)$/);
  if (m) env[m[1]] = m[2];
}
env.NODE_EXTRA_CA_CERTS =
  env.NODE_EXTRA_CA_CERTS || `${process.env.USERPROFILE}\\.gcloud-ca\\win-roots.pem`;

if (!env.DT_ENVIRONMENT || !env.DT_PLATFORM_TOKEN) {
  console.error("missing DT_ENVIRONMENT / DT_PLATFORM_TOKEN in .env");
  process.exit(2);
}

const child = spawn("npx", ["-y", "@dynatrace-oss/dynatrace-mcp-server@latest"], {
  env,
  stdio: ["pipe", "pipe", "pipe"],
  shell: true,
});
child.stderr.on("data", (d) => process.stderr.write("[server] " + d));

const pending = new Map();
let nextId = 1;
const send = (method, params = {}) => {
  const id = nextId++;
  child.stdin.write(JSON.stringify({ jsonrpc: "2.0", id, method, params }) + "\n");
  return new Promise((res) => pending.set(id, res));
};
const notify = (method, params = {}) =>
  child.stdin.write(JSON.stringify({ jsonrpc: "2.0", method, params }) + "\n");

readline.createInterface({ input: child.stdout }).on("line", (line) => {
  line = line.trim();
  if (!line.startsWith("{")) return;
  let msg;
  try {
    msg = JSON.parse(line);
  } catch {
    return;
  }
  if (msg.id && pending.has(msg.id)) {
    pending.get(msg.id)(msg);
    pending.delete(msg.id);
  }
});

const timer = setTimeout(() => {
  console.error("TIMEOUT waiting for MCP server");
  child.kill();
  process.exit(3);
}, 120000);

(async () => {
  const init = await send("initialize", {
    protocolVersion: "2024-11-05",
    capabilities: {},
    clientInfo: { name: "patchpilot-verify", version: "0.1.0" },
  });
  if (init.error) {
    console.error("initialize error:", JSON.stringify(init.error));
    child.kill();
    process.exit(1);
  }
  console.log("initialize OK ->", JSON.stringify(init.result?.serverInfo ?? {}));
  notify("notifications/initialized");
  const tools = await send("tools/list");
  const names = (tools.result?.tools ?? []).map((t) => t.name);
  console.log(`TOOLS (${names.length}):`);
  for (const n of names) console.log("  - " + n);
  clearTimeout(timer);
  child.kill();
  process.exit(0);
})().catch((e) => {
  console.error("ERR", e);
  child.kill();
  process.exit(1);
});
