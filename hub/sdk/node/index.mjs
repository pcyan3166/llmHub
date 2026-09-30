export class LLMHubError extends Error {
  constructor(status, data, requestId) {
    super(data?.error?.message ?? `llmHub returned HTTP ${status}`);
    this.status = status;
    this.code = data?.error?.code;
    this.requestId = requestId;
  }
}

export class LLMHub {
  constructor({ apiKey, baseURL = "http://127.0.0.1:8080/v1", timeoutMs = 900000 }) {
    if (!apiKey) throw new Error("llmHub project apiKey is required");
    this.apiKey = apiKey;
    this.baseURL = baseURL.replace(/\/$/, "");
    this.timeoutMs = timeoutMs;
  }
  async request(endpoint, scene, body, { signal } = {}) {
    if (!scene) throw new Error("scene is required");
    const response = await fetch(`${this.baseURL}/${endpoint}`, {
      method: "POST",
      headers: { Authorization: `Bearer ${this.apiKey}`, "Content-Type": "application/json" },
      body: JSON.stringify({ ...body, model: `scene/${scene}` }),
      signal: signal ? AbortSignal.any([signal, AbortSignal.timeout(this.timeoutMs)]) : AbortSignal.timeout(this.timeoutMs),
    });
    if (!response.ok) {
      let data;
      try { data = await response.json(); } catch { data = {}; }
      throw new LLMHubError(response.status, data, response.headers.get("X-Request-ID"));
    }
    // Returning Response preserves SSE, headers and SDK-compatible response bodies.
    return response;
  }
  async text(scene, messages, options = {}, requestOptions) {
    return (await this.request("chat/completions", scene, { ...options, messages, stream: false }, requestOptions)).json();
  }
  stream(scene, messages, options = {}, requestOptions) {
    return this.request("chat/completions", scene, { ...options, messages, stream: true }, requestOptions);
  }
  async responses(scene, input, options = {}, requestOptions) {
    return (await this.request("responses", scene, { ...options, input, stream: false }, requestOptions)).json();
  }
  async image(scene, prompt, options = {}, requestOptions) {
    return (await this.request("images/generations", scene, { ...options, prompt }, requestOptions)).json();
  }
  async embedding(scene, input, options = {}, requestOptions) {
    return (await this.request("embeddings", scene, { ...options, input }, requestOptions)).json();
  }
}
