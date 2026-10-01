"""Dependency-free llmHub client; OpenAI SDKs can also use the /v1 origin directly."""
import json
import urllib.request
import urllib.error


class LLMHubError(Exception):
    def __init__(self, status, data, request_id):
        super().__init__(data.get("error", {}).get("message", f"llmHub HTTP {status}"))
        self.status = status
        self.code = data.get("error", {}).get("code")
        self.request_id = request_id


class LLMHub:
    def __init__(self, api_key, base_url="http://127.0.0.1:8080/v1", timeout=900):
        if not api_key:
            raise ValueError("a project api_key is required")
        self.api_key = api_key
        self.base_url = base_url.rstrip("/")
        self.timeout = timeout

    def request(self, endpoint, scene, body):
        if not scene:
            raise ValueError("scene is required")
        payload = dict(body, model=f"scene/{scene}")
        request = urllib.request.Request(
            f"{self.base_url}/{endpoint}",
            data=json.dumps(payload, ensure_ascii=False).encode(),
            headers={"Authorization": f"Bearer {self.api_key}", "Content-Type": "application/json"},
        )
        try:
            with urllib.request.urlopen(request, timeout=self.timeout) as response:
                return json.load(response)
        except urllib.error.HTTPError as error:
            try:
                data = json.load(error)
            except (ValueError, UnicodeDecodeError):
                data = {}
            raise LLMHubError(error.code, data, error.headers.get("X-Request-ID")) from error

    def text(self, scene, messages, **options):
        return self.request("chat/completions", scene, dict(options, messages=messages, stream=False))

    def responses(self, scene, input, **options):
        return self.request("responses", scene, dict(options, input=input, stream=False))

    def image(self, scene, prompt, **options):
        return self.request("images/generations", scene, dict(options, prompt=prompt))

    def embedding(self, scene, input, **options):
        return self.request("embeddings", scene, dict(options, input=input))
