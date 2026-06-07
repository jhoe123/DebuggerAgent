// Dev tool: call a Dynatrace MCP tool and print the result.
//   node scripts/dt_query.mjs <toolName> '<jsonArgs>'
//   node scripts/dt_query.mjs                 # lists tools + schemas
import { spawn } from "node:child_process";
import { readFileSync } from "node:fs";
import path from "node:path";
import readline from "node:readline";

const env = { ...process.env };
for (const line of readFileSync(new URL("../.env", import.meta.url), "utf8").split(/\r?\n/)) {
  const m = line.match(/^([A-Z0-9_]+)=(.*)$/);
  if (m) env[m[1]] = m[2];
}
env.NODE_EXTRA_CA_CERTS =
  env.NODE_EXTRA_CA_CERTS || `${process.env.USERPROFILE}\\.gcloud-ca\\win-roots.pem`;

const toolName = process.argv[2];
const toolArgs = process.argv[3] ? JSON.parse(process.argv[3]) : {};

const nodeBin = env.MCP_NODE_BIN || "node";
const nodeDir = path.dirname(nodeBin);
const npxCli = path.join(nodeDir, "node_modules", "npm", "bin", "npx-cli.js");
env.PATH = nodeDir + path.delimiter + (env.PATH || "");
const child = spawn(nodeBin, [npxCli, "-y", "@dynatrace-oss/dynatrace-mcp-server@latest"], {
  env,
  stdio: ["pipe", "pipe", "pipe"],
});
child.stderr.on("data", () => {}); // silence server logs

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
  try { msg = JSON.parse(line); } catch { return; }
  if (msg.id && pending.has(msg.id)) {
    pending.get(msg.id)(msg);
    pending.delete(msg.id);
  }
});

const timer = setTimeout(() => { console.error("TIMEOUT"); child.kill(); process.exit(3); }, 120000);

(async () => {
  await send("initialize", {
    protocolVersion: "2024-11-05",
    capabilities: {},
    clientInfo: { name: "dt-query", version: "0.1.0" },
  });
  notify("notifications/initialized");

  if (!toolName) {
    const tools = await send("tools/list");
    for (const t of tools.result?.tools ?? []) {
      console.log(`\n## ${t.name}\n${t.description ?? ""}`);
      console.log("inputSchema:", JSON.stringify(t.inputSchema?.properties ?? {}, null, 2));
    }
  } else {
    const r = await send("tools/call", { name: toolName, arguments: toolArgs });
    console.log(JSON.stringify(r.result ?? r.error, null, 2));
  }
  clearTimeout(timer);
  child.kill();
  process.exit(0);
})().catch((e) => { console.error("ERR", e); child.kill(); process.exit(1); });
