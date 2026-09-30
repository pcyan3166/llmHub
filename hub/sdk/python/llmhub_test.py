import io
import json
import unittest
import urllib.error
from unittest.mock import patch
from llmhub import LLMHub, LLMHubError


class ClientTests(unittest.TestCase):
    def test_scene_and_credentials(self):
        with patch("urllib.request.urlopen") as open_url:
            open_url.return_value.__enter__.return_value = io.BytesIO(b'{"choices":[]}')
            result = LLMHub("project-key").text("product.description", [{"role": "user", "content": "hi"}])
            request = open_url.call_args.args[0]
            self.assertEqual(json.loads(request.data)["model"], "scene/product.description")
            self.assertEqual(request.get_header("Authorization"), "Bearer project-key")
            self.assertEqual(result, {"choices": []})

    def test_budget_error(self):
        error = urllib.error.HTTPError("http://localhost", 402, "budget", {"X-Request-ID": "req-test"}, io.BytesIO(b'{"error":{"code":"budget_exhausted","message":"budget"}}'))
        with patch("urllib.request.urlopen", side_effect=error):
            with self.assertRaises(LLMHubError) as caught:
                LLMHub("key").image("poster", "prompt")
            self.assertEqual(caught.exception.status, 402)
            self.assertEqual(caught.exception.code, "budget_exhausted")
            self.assertEqual(caught.exception.request_id, "req-test")


if __name__ == "__main__":
    unittest.main()
