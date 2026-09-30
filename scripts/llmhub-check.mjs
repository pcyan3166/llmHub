import { spawn } from "node:child_process";
import path from "node:path";
import { fileURLToPath } from "node:url";
const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const env = { ...process.env, GOTOOLCHAIN: "local", GOCACHE: process.env.LLMHUB_GO_CACHE || path.join(process.env.TMPDIR || "/tmp", "llmhub-go-cache") };
const checks = [
  ["Go race regression", "go", ["test", "-race", "-timeout", "60s", "./..."], "hub", 180000],
  ["Go vet", "go", ["vet", "./..."], "hub", 90000],
  ["Node client", "node", ["--test", "hub/sdk/node/index.test.mjs"], ".", 30000],
  ["Python client", process.env.LLMHUB_PYTHON || "python3", ["-m", "unittest", "discover", "-s", "hub/sdk/python", "-p", "*_test.py"], ".", 30000],
  ["Console build", "npm", ["run", "build:llmhub"], "ui", 180000],
  ["Console format", "npm", ["run", "format:llmhub", "--", "--check"], "ui", 30000],
];
for (const [label, command, args, directory, timeout] of checks) {
  console.log(`\n[llmHub] ${label}`);
  await new Promise((resolve, reject) => {
    const child = spawn(command, args, { cwd: path.join(root, directory), env, stdio: "inherit", detached: process.platform !== "win32" });
    const timer = setTimeout(() => { if (process.platform === "win32") child.kill("SIGKILL"); else process.kill(-child.pid, "SIGKILL"); reject(new Error(`${label} timed out after ${timeout / 1000}s`)); }, timeout);
    child.on("error", (error) => { clearTimeout(timer); reject(error); });
    child.on("exit", (code, signal) => { clearTimeout(timer); if (code === 0) resolve(); else reject(new Error(`${label} failed: ${code ?? signal}`)); });
  });
}
console.log("\nAll llmHub checks passed.");
