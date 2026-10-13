<p align="center">
  <img src="frontend/public/brand/tokenswoop-mark.svg" alt="TokenSwoop logo" width="88" />
</p>

# TokenSwoop

**LLM APIs with pay-after-use USDC settlement on Monad.**

TokenSwoop brings model access, wallet payments, and usage tracking into one workspace. Connect with a passkey or an external wallet, choose a model, and start building through familiar LLM APIs or the streaming Playground. Charges are based on usage confirmed by the model provider and collected after the request finishes.

[Website](https://tokenswoop.xyz) · [Mainnet app](https://app.tokenswoop.xyz) · [Testnet app](https://testnet.tokenswoop.xyz) · [Deployment guide](deploy/README.md)

Built for the Monad Metropolis Hackathon.

## Features

- **Multiple LLM protocols.** OpenAI Chat Completions and Responses, Anthropic Messages, and Google Gemini, with streaming, reasoning, and function tools where supported by the selected model and channel.
- **Payments after completion.** Confirmed upstream usage determines the bill. No prepaid platform credits or local token estimates.
- **Batch settlement on Monad.** Completed usage is grouped by time and Tier-based value thresholds to reduce settlement transactions and Gas costs. Persistent transaction journals and receipt verification support recovery without creating a second charge for the same bill.
- **Passkeys and wallets.** Mera passkey wallets and external EVM wallets use the verified wallet address as the account identity. Privy email and Google login are available when configured.
- **Sponsored wallet operations.** Mera users can authorize payments, add Reserve, withdraw, and revoke allowances with platform-paid Gas when sponsorship is enabled. External wallets pay their own Gas.
- **Reserve and Tiers.** Reserve deposits unlock configurable discounts, request limits, and concurrency. Users can withdraw available principal directly; reported unpaid debt freezes only the corresponding amount.
- **A complete workspace.** Model library, streaming Playground, project API keys, usage history, bill details, onchain transaction links, and light/dark themes.
- **Operator controls.** Configure upstream channels, import and map models, manage token/cache/tool pricing, inspect request attempts and performance, and review settlement status.
- **Separate network profiles.** Switch a deployment with `CHAIN_NETWORK=testnet` or `mainnet`. Testnet uses kUSDC and an optional Turnstile-protected faucet; mainnet uses native USDC.

## How it works

1. A user connects a wallet and authorizes a payment allowance for the payment contract.
2. TokenSwoop routes an API request to a configured upstream channel and streams the response.
3. The gateway records confirmed usage, the applicable price, and the user's Tier discount after completion.
4. The settlement worker groups completed charges and collects from the wallet on Monad. Users can inspect the bill and its transaction in the workspace.

Normal settlement queues and pending confirmations do not interrupt accepted requests. A collection failure caused by insufficient balance or allowance pauses new paid requests until the failed bill is paid. Reserve principal is separate from API payment funds.

## Quick start

### Requirements

- Go 1.26
- Node.js 24 and npm
- PostgreSQL 18 and Redis 7+
- Bash, Make, and OpenSSL

Run these commands from the repository root:

```sh
npm --prefix frontend ci
make init
```

`make init` creates `.env.local`, generates an application secret, and starts project-local PostgreSQL on port `55432` and Redis on port `6389`. It creates separate databases for mainnet and testnet. On systems without Homebrew's PostgreSQL 18 path, set `PG_BIN` to the directory containing `initdb`, `pg_ctl`, `psql`, and `createdb` before running it. Run this development setup as a regular user.

Edit `.env.local`:

- Set `ADMIN_WALLETS` to the wallet addresses that should have platform administrator access.
- Keep `CHAIN_NETWORK=testnet` for a new test deployment.
- Keep `PUBLIC_ORIGIN=http://localhost:5173` for the default development URL.
- Follow the [deployment guide](deploy/README.md) to configure chain contracts, signers, and optional wallet services before enabling paid requests.

Start each process in a separate terminal:

```sh
make api
```

```sh
make worker
```

```sh
make web
```

Open [the local workspace](http://localhost:5173/app). The landing page is at [localhost:5173](http://localhost:5173). The frontend proxies API traffic to port `8080`.

Sign in with an administrator wallet, add an upstream in **Operations → Channels**, fetch or enter model IDs, and save their public mappings. Set prices and enable the models in **Models**. Imported models start as disabled drafts.

## API usage

Create a project key in the workspace, authorize a payment allowance for paid models, and choose a public model ID from the model library.

```sh
export TOKENSWOOP_API_KEY="sk-YOUR_TOKENSWOOP_KEY"

curl -N https://app.tokenswoop.xyz/v1/chat/completions \
  -H "Authorization: Bearer ${TOKENSWOOP_API_KEY}" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "your-model-id",
    "messages": [{"role": "user", "content": "Hello!"}],
    "stream": true
  }'
```

Use `https://testnet.tokenswoop.xyz` for the hosted testnet or `http://localhost:8080` for your local API. Keys, models, and billing data belong to their respective deployments.

| Protocol                | Endpoint                                            |
| ----------------------- | --------------------------------------------------- |
| Model catalog           | `GET /v1/models`                                    |
| OpenAI Chat Completions | `POST /v1/chat/completions`                         |
| OpenAI Responses        | `POST /v1/responses`                                |
| Anthropic Messages      | `POST /v1/messages`                                 |
| Gemini                  | `POST /v1beta/models/{model}:generateContent`       |
| Gemini streaming        | `POST /v1beta/models/{model}:streamGenerateContent` |

The workspace's **Quickstart** page includes client examples for SDKs, Claude Code, and Codex. Protocol-specific fields depend on the selected channel; conversions that cannot preserve their meaning are rejected. Image, audio, embeddings, Realtime, and WebSocket APIs are outside the current scope.

## Project structure

| Directory        | Purpose                                                                          |
| ---------------- | -------------------------------------------------------------------------------- |
| `frontend/`      | React, TypeScript, and Vite landing page and workspace                           |
| `backend/`       | Go API, routing, pricing, PostgreSQL ledger, migrations, and settlement worker   |
| `contracts/`     | Solidity payment, Reserve, Mera account, and test-token contracts; Foundry tests |
| `kusdc-console/` | Independent testnet token administration UI                                      |
| `deploy/`        | Deployment guide, systemd units, Nginx configuration, and container scaffolding  |
| `scripts/`       | Local development, verification, deployment helpers, and secret scanning         |

## Development

```sh
make test          # Frontend, script, and isolated backend integration tests
make build         # Go build and frontend TypeScript/production build
make stop          # Stop the project-local PostgreSQL and Redis instances
make secrets-check # Scan the files eligible for source control
```

Run `make init` before integration tests and keep the local database services available. `make test` uses test fixtures and does not broadcast blockchain transactions. Secret scanning requires Python 3 and Gitleaks 8.25+.

Contract tooling and local security tests are covered in the [deployment guide](deploy/README.md#contract-tooling). For the independent kUSDC console, install its dependencies with `npm --prefix kusdc-console ci`, then run `make kusdc-web` or `make kusdc-build`.

Before a commit, stage your changes and check the exact index contents:

```sh
python3 scripts/check-secrets.py --staged
```

Environment files, wallet material, databases, transaction journals, build output, and generated media are excluded by [.gitignore](.gitignore). Keep secrets out of source files and browser-facing `VITE_*` variables. To contribute, open an issue or pull request with a clear description and the relevant verification results.
