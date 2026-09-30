// Optional integration with a real Bifrost executable and a local mock provider.
import assert from "node:assert/strict";
import { mkdtemp, writeFile, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import { spawn } from "node:child_process";
import { createServer } from "node:http";
import { LLMHub } from "../../../hub/sdk/node/index.mjs";

assert.ok(process.env.BIFROST_BINARY, "Set BIFROST_BINARY to an official Bifrost executable");
assert.ok(process.env.LLMHUB_BINARY, "Set LLMHUB_BINARY to a compiled hub/cmd/llmhub executable");
const directory = await mkdtemp(path.join(tmpdir(), "llmhub-native-"));
const processes = [];
const requests = [];
const mock = createServer(async (req, res) => {
  let raw = "";
  for await (const chunk of req) raw += chunk;
  const b = raw ? JSON.parse(raw) : {};
  requests.push({ path: req.url, model: b.model, authorization: req.headers.authorization });
  if (req.url?.includes("models")) { res.setHeader("Content-Type", "application/json"); res.end(JSON.stringify({ object: "list", data: [{ id: "mock-text", object: "model", owned_by: "mock" }] })); return; }
  if (b.stream) { res.setHeader("Content-Type", "text/event-stream"); res.end('data: {"id":"mock-chat","object":"chat.completion.chunk","model":"mock-text","choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":null}]}\n\ndata: {"id":"mock-chat","object":"chat.completion.chunk","model":"mock-text","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}\n\ndata: [DONE]\n\n'); return; }
  res.setHeader("Content-Type", "application/json");
  res.end(JSON.stringify({ id: "mock-chat", object: "chat.completion", created: 1, model: "mock-text", choices: [{ index: 0, message: { role: "assistant", content: "ok" }, finish_reason: "stop" }], usage: { prompt_tokens: 10, completion_tokens: 5, total_tokens: 15 } }));
});
await new Promise((resolve) => mock.listen(0, "127.0.0.1", resolve));
const port = mock.address().port;
const admin = "native-integration-admin-token-only";
const watchdog = setTimeout(() => { for (const p of processes) p.kill("SIGKILL"); console.error("Native integration exceeded 60 seconds"); process.exit(1); }, 60000);
async function freePort() { const server = createServer(); await new Promise((r) => server.listen(0, "127.0.0.1", r)); const port = server.address().port; await new Promise((r) => server.close(r)); return port; }
async function ready(url, child) {
  const limit = Date.now() + 25000;
  while (Date.now() < limit) {
    if (child.exitCode !== null) throw new Error(`service exited: ${child.exitCode}`);
    try { const res = await fetch(`${url}/health`, { signal: AbortSignal.timeout(1000) }); if (res.ok) return; } catch { /* Still starting. */ }
    await new Promise((r) => setTimeout(r, 200));
  }
  throw new Error(`service did not become healthy: ${url}`);
}
try {
  const bifrostPort = await freePort();
  const hubPort = await freePort();
  const bifrostURL = `http://127.0.0.1:${bifrostPort}`;
  const hubURL = `http://127.0.0.1:${hubPort}`;
  await writeFile(path.join(directory, "config.json"), JSON.stringify({ client: { initial_pool_size: 5, enable_logging: false, drop_excess_requests: false }, config_store: { enabled: false }, logs_store: { enabled: false }, providers: { openai: { keys: [{ name: "primary", value: "mock-provider-key", models: ["*"], weight: 1 }], network_config: { base_url: `http://127.0.0.1:${port}/v1`, max_retries: 0 }, concurrency_and_buffer_size: { concurrency: 2, buffer_size: 10 } } } }));
  await writeFile(path.join(directory, "seed.json"), JSON.stringify({ projects: [{ id: "p", name: "Integration", enabled: true, monthly_budget_usd: 1 }], pools: [{ id: "shared", concurrency: 1, queue_size: 4, rpm: 50, tpm: 100000 }], profiles: [{ id: "text.fast", provider: "openai", model: "mock-text", key_name: "primary", pool_id: "shared", max_output_tokens: 32, input_usd_per_million: 1, output_usd_per_million: 2 }], scenes: [{ id: "copy", project_id: "p", name: "Copy", profiles: ["text.fast"], endpoint: "/v1/chat/completions", queue_timeout_seconds: 3, timeout_seconds: 5, retries: 0 }] }));
  const bifrost = spawn(process.env.BIFROST_BINARY, ["-host", "127.0.0.1", "-port", String(bifrostPort), "-app-dir", directory], { env: { ...process.env, BIFROST_SETUP_TOKEN: admin }, stdio: ["ignore", "pipe", "pipe"] });
  let logs = "";
  bifrost.stdout.on("data", (b) => { logs = (logs + b).slice(-12000); });
  bifrost.stderr.on("data", (b) => { logs = (logs + b).slice(-12000); });
  processes.push(bifrost);
  try { await ready(bifrostURL, bifrost); } catch (e) { console.error(logs); throw e; }
  const hub = spawn(process.env.LLMHUB_BINARY, ["-listen", `127.0.0.1:${hubPort}`, "-db", path.join(directory, "hub.db"), "-bifrost", bifrostURL, "-seed", path.join(directory, "seed.json"), "-ui", ""], { env: { ...process.env, LLMHUB_ADMIN_TOKEN: admin }, stdio: "ignore" });
  processes.push(hub);
  await ready(hubURL, hub);
  const keyResponse = await fetch(`${hubURL}/api/llmhub/keys`, { method: "POST", headers: { Authorization: `Bearer ${admin}`, "Content-Type": "application/json" }, body: JSON.stringify({ project_id: "p" }) });
  assert.equal(keyResponse.status, 201);
  const { token } = await keyResponse.json();
  const client = new LLMHub({ apiKey: token, baseURL: `${hubURL}/v1`, timeoutMs: 5000 });
  const result = await client.text("copy", [{ role: "user", content: "hello" }]);
  assert.equal(result.choices[0].message.content, "ok");
  const stream = await client.stream("copy", [{ role: "user", content: "hello" }]);
  assert.match(await stream.text(), /\[DONE\]/);
  const overview = await (await fetch(`${hubURL}/api/llmhub/overview`, { headers: { Authorization: `Bearer ${admin}` } })).json();
  assert.equal(overview.usage[0].attempts, 2);
  assert.equal(overview.usage[0].input_tokens, 20);
  assert.equal(overview.usage[0].output_tokens, 10);
  assert.equal(overview.usage[0].cost_usd, 0.00004);
  assert.equal(overview.recent[0].price_snapshot.input_usd_per_million, 1);
  const inferenceRequests = requests.filter((r) => r.path?.includes("chat/completions"));
  assert.equal(inferenceRequests.length, 2);
  assert.ok(inferenceRequests.every((r) => r.model === "mock-text" && r.authorization === "Bearer mock-provider-key"));
  console.log("PASS: real Bifrost -> local mock provider; project authentication, alias, selected provider key, unary/SSE and exact usage settlement");
} finally {
  clearTimeout(watchdog);
  await Promise.all(processes.map((p) => new Promise((resolve) => { if (p.exitCode !== null) return resolve(); p.once("exit", resolve); p.kill("SIGTERM"); setTimeout(() => p.kill("SIGKILL"), 2000).unref(); })));
  await new Promise((resolve) => mock.close(resolve));
  await rm(directory, { recursive: true, force: true });
}
