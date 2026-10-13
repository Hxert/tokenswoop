# Deployment

This guide covers a native Go + Nginx + systemd deployment. Commands are run from the repository root unless stated otherwise. Configuration examples use placeholders; provide your own database credentials, RPC endpoints, contract addresses, and signing wallets.

## Runtime layout

| Component                 | Requirement                                                                          |
| ------------------------- | ------------------------------------------------------------------------------------ |
| API and settlement worker | Go 1.26 to build; two separate processes at runtime                                  |
| Web build                 | Node.js 24 and npm; deploy only `frontend/dist/`                                     |
| Database                  | PostgreSQL 18; one database per network                                              |
| Cache and coordination    | Redis 7+; separate Redis databases per network                                       |
| HTTP and TLS              | Nginx and a certificate covering every public hostname                               |
| Transaction signer        | Foundry `cast`, encrypted keystore, and a password file readable by the service user |

The supplied [systemd units](systemd/) expect a `tokenswoop` service user, `/opt/tokenswoop/current`, `/etc/tokenswoop/`, and the PostgreSQL unit `postgresql@18-main.service`. Adapt those paths and service dependencies to your distribution.

```text
/opt/tokenswoop/current/
  bin/api
  bin/worker
  web/                  # Contents of frontend/dist
/etc/tokenswoop/
  runtime.env           # Shared application settings
  mainnet.env           # Mainnet profile and origin
  testnet.env           # Testnet profile and origin
  secrets/              # Signer keystores and password files
/var/lib/tokenswoop/     # Service runtime directory
```

Keep environment and signer files outside the web root and readable only by their owner/service group. Include the backend and RelayKit license notices with backend distributions.

## Build

Build on the target platform or with the appropriate Go target settings:

```sh
npm --prefix frontend ci
npm --prefix frontend run build
mkdir -p backend/bin
(
  cd backend
  CGO_ENABLED=0 go build -trimpath -o bin/api ./cmd/api
  CGO_ENABLED=0 go build -trimpath -o bin/worker ./cmd/worker
)
```

Install the two binaries in `bin/` and the contents of `frontend/dist/` in `web/` under your release directory. Point `/opt/tokenswoop/current` to that release. Keep the repository, `.env` files, and contract output outside the published directory.

## Environment and network selection

Use [`.env.example`](../.env.example) as the complete configuration reference. Local tools read `.env.local`; production systemd services read the files shown above. Changing the local file does not update a running server.

Shared settings in `/etc/tokenswoop/runtime.env`:

```dotenv
APP_ENV=production
APP_SECRET=<base64-encoded-32-byte-secret>
ADMIN_WALLETS=<comma-separated-admin-wallet-addresses>
ALLOW_PRIVATE_UPSTREAMS=false
TRUSTED_PROXY_CIDRS=127.0.0.1/32,::1/128
SOURCE_URL=
```

Generate `APP_SECRET` with `openssl rand -base64 32` and store it securely. Keep it stable across restarts and releases: it protects sessions, API keys, upstream credentials, and signed transaction journals. `ADMIN_WALLETS` controls platform administration; contract management roles are configured separately. `SOURCE_URL` accepts a public HTTPS location for the corresponding backend source and is optional at runtime; preserve the included license notices.

Create and provision a PostgreSQL database for each network. Populate the network-prefixed database/Redis URLs in the corresponding instance file. For example:

```dotenv
# /etc/tokenswoop/mainnet.env
CHAIN_NETWORK=mainnet
LISTEN_ADDR=127.0.0.1:8080
PUBLIC_ORIGIN=https://app.tokenswoop.xyz
MAINNET_DATABASE_URL=<mainnet-postgresql-connection-url>
MAINNET_REDIS_URL=redis://127.0.0.1:6379/1
MAINNET_SETTLEMENT_ENABLED=false
MAINNET_RESERVE_ENABLED=false
```

```dotenv
# /etc/tokenswoop/testnet.env
CHAIN_NETWORK=testnet
LISTEN_ADDR=127.0.0.1:8081
PUBLIC_ORIGIN=https://testnet.tokenswoop.xyz
TESTNET_DATABASE_URL=<testnet-postgresql-connection-url>
TESTNET_REDIS_URL=redis://127.0.0.1:6379/0
TESTNET_SETTLEMENT_ENABLED=false
TESTNET_RESERVE_ENABLED=false
```

Complete the contract and signing configuration below before enabling settlement. Database migrations run when the API or worker starts. Stop old instances before upgrading a schema.

For a deployment with both profiles already configured, change only `CHAIN_NETWORK=testnet|mainnet` and restart **both** API and worker. The frontend reads `/api/config`; no frontend rebuild is needed to change networks. Update `PUBLIC_ORIGIN` if the deployment's URL also changes. Mainnet is chain `143`; testnet is `10143`. Do not add a conflicting `CHAIN_ID`.

Keep databases, API keys, bills, and wallet state separate between networks. The database records its chain ID and rejects reuse on the other network. Running both public sites at once requires separate API/worker instances and ports.

## Payment and Reserve configuration

For the selected profile, use the `MAINNET_` or `TESTNET_` prefix on every setting below:

| Settings                                                          | Purpose                                                                                       |
| ----------------------------------------------------------------- | --------------------------------------------------------------------------------------------- |
| `PAYMENT_RPC_URL`, `PAYMENT_RPC_RPS`                              | Private HTTPS endpoint and shared request-rate budget; set a limit supported by your provider |
| `PAYMENT_CONTRACT`, `PAYMENT_TOKEN`                               | Batch payment contract and six-decimal settlement token                                       |
| `PAYMENT_PROVIDER`, `PAYMENT_RECIPIENT`, `PAYMENT_STOP_AUTHORITY` | Collection signer, revenue wallet, and independent authority able to pause/resume collection  |
| `PAYMENT_CONTRACT_CODE_HASH`, `PAYMENT_TOKEN_CODE_HASH`           | Verified deployed runtime bytecode hashes                                                     |
| `PAYMENT_CAST_PATH`, `PAYMENT_KEYSTORE`, `PAYMENT_PASSWORD_FILE`  | Absolute server paths to `cast` and the collection signer's files                             |
| `RESERVE_CONTRACT`, `RESERVE_CODE_HASH`                           | Reserve deployment and verified runtime code hash                                             |
| `RESERVE_MODE`, `RESERVE_CUSTODY`                                 | `debt` mode; `vault` for direct custody, or `aave` only for a compatible testnet deployment   |
| `RESERVE_OPERATOR`, `RESERVE_MANAGER`                             | Debt-reporting signer and independent management authority                                    |
| `RESERVE_KEYSTORE`, `RESERVE_PASSWORD_FILE`                       | Absolute server paths to the reporting signer's files                                         |

Set `SETTLEMENT_ENABLED=true` and `RESERVE_ENABLED=true` only after verification. The API verifies the configured chain/contracts; the worker also verifies its signers. Reserve requires settlement. Vault custody does not use a Pool, aToken, or treasury setting.

Mainnet uses native USDC, as pinned in the example configuration. `TokenSwoopReserveVault` holds principal directly and has no Aave integration. API fees go to the payment recipient; they are not deducted from Reserve principal. Available principal can be withdrawn without platform approval, while confirmed reported debt remains frozen.

The provider and stop authority must be different addresses, as must the debt operator and Reserve manager. Mainnet also requires a revenue recipient separate from the provider. These contract roles are fixed at deployment. Fund transaction-signing wallets with MON; customers need the selected settlement token. The revenue recipient does not need MON just to receive collections.

Use one shared ledger/worker coordination domain for each signing wallet on a chain. Do not run an independent development database against a live production signing wallet.

### Contract tooling

Versions and dependencies are pinned in [`contracts/dependencies.lock.json`](../contracts/dependencies.lock.json); build settings are in [`foundry.toml`](../contracts/foundry.toml).

On macOS arm64, the supplied bootstrap downloads and verifies the pinned toolchain and libraries:

```sh
node contracts/scripts/bootstrap.mjs
(
  cd contracts
  ./.toolchain/bin/forge build
  ./.toolchain/bin/forge test
)
make reserve-security
```

On other platforms, install the locked Foundry and Solidity versions for that platform in `contracts/.toolchain/bin/`, including the `solc-0.8.37` executable. Fetch the locked OpenZeppelin and forge-std revisions into the `contracts/lib/` paths used by the remappings. The bootstrap's binary checksums target macOS arm64 and are not Linux installers.

The normal contract tests run locally. Network deployment scripts and explicit chain tests are separate operations and can spend real assets.

### Mainnet contracts

Populate the mainnet role fields in `.env.local`, including `MAINNET_DEPLOYER_ADDRESS`, and build the contracts. Run the read-only checks and local fork validation:

```sh
make mainnet-check
make mainnet-fork-test
make mainnet-funding
```

For a **new deployment**, simulate the deployment before broadcasting:

```sh
(
  set -a
  . ./.env.local
  set +a
  cd contracts
  ./.toolchain/bin/forge script script/DeployReserveDebt.s.sol:DeployReserveDebtMainnet \
    --rpc-url "$MAINNET_PAYMENT_RPC_URL" --sender "$MAINNET_DEPLOYER_ADDRESS"
)
```

This entry deploys Batch payment and ReserveVault contracts. After reviewing the simulation, broadcast with the deployer's `--keystore` and `--password-file` arguments. If a broadcast is interrupted, inspect and resume its existing Foundry journal rather than starting a new deployment. Initial deployment needs MON for Gas and no USDC seed deposit.

For finalized deployments, set `MAINNET_PAYMENT_CONTRACT` and `MAINNET_RESERVE_CONTRACT` in `.env.local`, then run:

```sh
node scripts/configure-mainnet.mjs
```

This helper verifies the runtime bytecode, token, roles, and custody model, writes a local deployment manifest, and enables the mainnet settlement/Reserve profile. It does not broadcast or change `CHAIN_NETWORK`. Copy the verified settings to the server, replacing all local signer paths with server paths. Reusing an existing deployment does not require redeploying its contracts.

### Testnet contracts

For a new direct-custody testnet deployment, use `DeployReserveVaultTestnet` in [`DeployReserveDebt.s.sol`](../contracts/script/DeployReserveDebt.s.sol). It uses the same vault model with a configured six-decimal test token. Its deployment inputs are:

- `DEPLOYER_ADDRESS`, `PAYMENT_TOKEN_ADDRESS`
- `PROVIDER_ADDRESS`, `PAYMENT_RECIPIENT_ADDRESS`, `PAYMENT_STOP_AUTHORITY_ADDRESS`
- `RESERVE_MANAGER_ADDRESS`, `SETTLEMENT_OPERATOR_ADDRESS`

Simulate with the testnet RPC and deployer before using `--broadcast`; configure `TESTNET_RESERVE_MODE=debt` and `TESTNET_RESERVE_CUSTODY=vault` after verifying the resulting contracts. Set the testnet token, payment/Reserve addresses, roles, runtime hashes, and signers in the corresponding profile.

The existing `make configure-testnet-payments` and `make configure-testnet-reserve` helpers verify deployments recorded in local manifests. They require `contracts/deployments/testnet-wallets.json`, the relevant deployment manifests, compiled artifacts, and keystores. Those files are installation-specific and intentionally excluded from Git. A fresh clone does not contain funded wallets or ready-made chain configuration.

## Optional wallet services

### Mera passkeys and Gas sponsorship

Passkey login needs HTTPS (or localhost) and an authenticator that supports WebAuthn PRF; no Mera App ID or server secret is required. Fix the public hostname before onboarding users: passkeys are scoped to that hostname. Do not assume a passkey created on localhost or another subdomain will work on the production host.

Sponsorship additionally needs a verified [`TokenSwoopMeraAccount`](../contracts/src/TokenSwoopMeraAccount.sol) implementation bound to that network's token, payment, and Reserve contracts. Configure the selected profile's `MERA_IMPLEMENTATION`, `MERA_CODE_HASH`, and `MERA_GAS_SPONSORSHIP_ENABLED=true`.

`MERA_SPONSOR_MODE=platform` reuses the payment provider and its keystore/password files. `dedicated` uses `MERA_SPONSOR_ADDRESS`, `MERA_KEYSTORE`, and `MERA_PASSWORD_FILE`. Configure `MERA_DAILY_TRANSACTIONS`, `MERA_MAX_GAS_WEI`, and `MERA_DAILY_GAS_WEI` for your budget, and fund the sponsor with MON. Gas budget values are in wei.

The [deployment helper](../scripts/mera-sponsor-deploy.mjs) reads local wallet/deployment journals and broadcasts transactions. Its mainnet path also requires a completed local testnet sponsorship report. Prepare those installation-specific inputs before using it; it is not a fresh-clone bootstrap command.

### Privy

Set `PRIVY_APP_ID` and `PRIVY_APP_SECRET` together to enable email/Google login and embedded wallets. Register the exact public origins and selected Monad network in Privy. Network-prefixed overrides are available if you use separate applications.

To enable sponsorship, configure App pays and its budget in Privy, then set `PRIVY_GAS_SPONSORSHIP_ENABLED=true` and `PRIVY_DAILY_SPONSORED_TRANSACTIONS`. The integration uses backend-validated operations and embedded-wallet signatures; do not enable arbitrary client-requested sponsorship. Keep the App Secret on the backend. Update the Nginx CSP if you use custom authentication domains.

### Testnet faucet

Configure the following testnet-only settings:

```dotenv
TESTNET_FAUCET_ENABLED=true
TESTNET_TURNSTILE_SITE_KEY=<public-site-key>
TESTNET_TURNSTILE_SECRET_KEY=<server-secret-key>
TESTNET_FAUCET_COOLDOWN_HOURS=24
TESTNET_FAUCET_IP_DAILY_LIMIT=10
TESTNET_FAUCET_DAILY_LIMIT=100
```

Allow the testnet hostname in the Turnstile widget. Settlement must be configured, and the testnet provider needs kUSDC to distribute plus MON for Gas. Each successful claim sends 10 kUSDC. The faucet is not exposed in mainnet mode. The supplied Nginx CSP allows the Turnstile script and frame; never put the secret key in frontend variables.

## Domains, Nginx, and TLS

The supplied configuration serves:

| Hostname                 | Page                     | Backend          |
| ------------------------ | ------------------------ | ---------------- |
| `tokenswoop.xyz`         | Landing page             | None             |
| `app.tokenswoop.xyz`     | Mainnet workspace at `/` | `127.0.0.1:8080` |
| `testnet.tokenswoop.xyz` | Testnet workspace at `/` | `127.0.0.1:8081` |

For your own domains, update both the Nginx host map/server names and [`frontend/src/siteRouting.ts`](../frontend/src/siteRouting.ts), then rebuild the frontend. Also update `PUBLIC_ORIGIN`, allowed authentication origins, and Turnstile hostnames. Unknown hostnames otherwise keep the local `/` landing page and `/app` workspace routing.

Use [`tokenswoop-http.conf`](nginx/tokenswoop-http.conf) for the initial ACME challenge if needed. Issue a certificate whose Subject Alternative Names cover **all** served hostnames, then install [`tokenswoop.conf`](nginx/tokenswoop.conf). A certificate named after the apex domain must still include both workspace subdomains. Configure automatic renewal and verify it.

Load [`tokenswoop-log.conf`](nginx/tokenswoop-log.conf) in Nginx's `http` context before the site configuration. Its access log format omits query strings, which can contain API credentials. The site configuration uses `http2 on`; use compatible Nginx syntax for your installed version.

Preserve same-origin API proxying, disabled SSE buffering, streaming timeouts, the SPA fallback, and the private-file deny rules. Validate with `nginx -t` before reloading. Expose HTTPS publicly; keep the Go API, PostgreSQL, and Redis bound to private interfaces.

## Start, verify, and update

Install the supplied units in `/etc/systemd/system/`, create the service user/directories, and set file permissions before starting. For a deployment running both networks:

```sh
sudo systemctl daemon-reload
sudo systemctl enable --now tokenswoop-api@mainnet tokenswoop-worker@mainnet
sudo systemctl enable --now tokenswoop-api@testnet tokenswoop-worker@testnet
```

Verify API health and network selection:

```sh
curl --fail https://app.tokenswoop.xyz/health/ready
curl --fail https://app.tokenswoop.xyz/api/config
curl --fail https://testnet.tokenswoop.xyz/health/ready
curl --fail https://testnet.tokenswoop.xyz/api/config
sudo journalctl -u tokenswoop-worker@mainnet -n 100 --no-pager
```

Check the worker heartbeat in the administrator's Operations page; API readiness alone does not confirm that settlement is running. Configure channels/models in each network's own administrator workspace. Test wallet login, a streaming model call, and the resulting bill/transaction using the intended network and a small amount.

For upgrades, build and verify a new release, stop that network's old API and worker, switch the release, then restart both. Keep the database, application secret, and transaction journals consistent. Pending transactions must be reconciled using their saved IDs, hashes, and signed bytes; do not delete journals or create new bills to resolve an unknown transaction result.

## Other deployment artifacts

- **kUSDC console:** `npm --prefix kusdc-console ci`, then `make kusdc-build`. Publish only `kusdc-console/dist/`. Configure the public test-token/RPC settings from its [environment example](../kusdc-console/.env.example). This console is testnet-only and uses the browser's token-owner wallet.
- **Containers:** [`compose.yaml`](compose.yaml) and the component Dockerfiles are scaffolding. The Compose settings use older unprefixed network variables and do not include signer tooling/secret mounts. Adapt them to the selected network profiles before using them; the production layout documented here uses systemd.
