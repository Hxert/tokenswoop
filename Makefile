.PHONY: init api worker web test build stop testnet-billing-prepare testnet-billing-settle testnet-billing-status
TESTNET_RUN ?= phase-one
WALLET_TEST_RUN ?= wallet_v1
init:
	bash scripts/dev.sh init
api:
	bash scripts/dev.sh api
worker:
	bash scripts/dev.sh worker
web:
	cd frontend && npm run dev
.PHONY: kusdc-web kusdc-build
kusdc-web:
	cd kusdc-console && npm run dev
kusdc-build:
	cd kusdc-console && npm run build
test:
	cd frontend && npm test
	node --test scripts/*.test.mjs
	bash scripts/dev.sh test
build:
	cd backend && GOCACHE="$$PWD/.cache/go-build" GOMODCACHE="$$PWD/.cache/gomod" GOPATH="$$PWD/.cache/gopath" go build ./cmd/...
	cd frontend && npm run build
stop:
	bash scripts/dev.sh stop
testnet-billing-prepare:
	node scripts/testnet-billing.mjs prepare --run "$(TESTNET_RUN)"
testnet-billing-settle:
	node scripts/testnet-billing.mjs settle --run "$(TESTNET_RUN)"
testnet-billing-status:
	node scripts/testnet-billing.mjs status --run "$(TESTNET_RUN)"

.PHONY: configure-testnet-payments testnet-wallet-payments
configure-testnet-payments:
	node scripts/configure-testnet-payments.mjs
testnet-wallet-payments:
	node scripts/wallet-payment-test.mjs "$(WALLET_TEST_RUN)"

.PHONY: configure-testnet-reserve
configure-testnet-reserve:
	node scripts/configure-testnet-reserve.mjs

.PHONY: testnet-reserve
RESERVE_TEST_RUN ?= reserve_workspace_v1
testnet-reserve:
	node scripts/reserve-testnet-test.mjs "$(RESERVE_TEST_RUN)"

.PHONY: reserve-security
# Local EVM only: no signer, RPC, or real assets are needed.
RESERVE_FUZZ_SEED ?= 0x20261010
reserve-security:
	cd contracts && FOUNDRY_PROFILE=reserve-security ./.toolchain/bin/forge test --match-path 'test/ReserveDebt*.t.sol' --fuzz-seed "$(RESERVE_FUZZ_SEED)"

.PHONY: mainnet-check mainnet-fork-test
# Read-only mainnet RPC; all money movement in the fork suite is local only.
mainnet-check:
	node scripts/check-mainnet.mjs
mainnet-fork-test:
	node scripts/check-mainnet.mjs
	node scripts/test-mainnet-fork.mjs

.PHONY: mainnet-funding
mainnet-funding:
	node scripts/estimate-mainnet-funding.mjs

.PHONY: secrets-check
PYTHON ?= python3
secrets-check:
	$(PYTHON) scripts/check-secrets.py
