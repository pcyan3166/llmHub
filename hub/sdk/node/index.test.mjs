import { test } from "node:test";
import assert from "node:assert/strict";
import { LLMHub, LLMHubError } from "./index.mjs";

test("client resolves scenes and preserves the OpenAI shape", async () => {
  const original = globalThis.fetch;
  let request;
  globalThis.fetch = async (url, init) => { request = { url, ...init }; return new Response(JSON.stringify({ choices: [{ message: { content: "ok" } }] }), { status: 200 }); };
  try {
    const hub = new LLMHub({ apiKey: "project-key" });
    const result = await hub.text("product.description", [{ role: "user", content: "hi" }], { model: "ignored" });
    assert.equal(result.choices[0].message.content, "ok");
    assert.equal(request.url, "http://127.0.0.1:8080/v1/chat/completions");
    assert.equal(JSON.parse(request.body).model, "scene/product.description");
    assert.equal(request.headers.Authorization, "Bearer project-key");
  } finally { globalThis.fetch = original; }
});
test("client preserves actionable gateway errors", async () => {
  const original = globalThis.fetch;
  globalThis.fetch = async () => new Response(JSON.stringify({ error: { code: "budget_exhausted", message: "budget exhausted" } }), { status: 402, headers: { "X-Request-ID": "req-test" } });
  try { await assert.rejects(new LLMHub({ apiKey: "key" }).image("poster", "prompt"), (err) => err instanceof LLMHubError && err.status === 402 && err.code === "budget_exhausted" && err.requestId === "req-test"); }
  finally { globalThis.fetch = original; }
});
test("stream returns a consumable response", async () => {
  const original = globalThis.fetch;
  globalThis.fetch = async () => new Response("data: [DONE]\n\n", { headers: { "Content-Type": "text/event-stream" } });
  try { const result = await new LLMHub({ apiKey: "key" }).stream("chat", []); assert.equal(await result.text(), "data: [DONE]\n\n"); }
  finally { globalThis.fetch = original; }
});
test("JSON responses helper cannot accidentally request SSE", async () => {
  const original = globalThis.fetch;
  let body;
  globalThis.fetch = async (_url, init) => { body = JSON.parse(init.body); return new Response(JSON.stringify({ output: [] })); };
  try {
    const result = await new LLMHub({ apiKey: "key" }).responses("reply", "hello", { stream: true });
    assert.deepEqual(result.output, []);
    assert.equal(body.stream, false);
  } finally { globalThis.fetch = original; }
});
