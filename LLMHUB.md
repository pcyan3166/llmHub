# llmHub

Bifrost-based single-instance gateway for shared project authentication, scene routing,
rate-aware queues, budgets, usage and costs. The upstream Bifrost engine is preserved.

- [Setup, console, API and SDK guide](docs/llmhub/README.md)
- [Verification and recovery record](docs/llmhub/status.md)
- [Deployment templates](deploy/llmhub/)

```bash
npm --prefix ui ci --ignore-scripts
npm --prefix ui run build:llmhub
cd hub
go run ./cmd/demo -listen 127.0.0.1:8090
```

Local mock preview: http://127.0.0.1:8090

Demo-only admin token: `llmhub-local-demo-admin-token`.

Validation from repository root: `node scripts/llmhub-check.mjs`.
