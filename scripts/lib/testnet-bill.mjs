import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';

export function units(value) {
  assert(typeof value === 'string' && /^\d{1,20}(\.\d{1,18})?$/.test(value), 'Use a nonnegative decimal string');
  const [whole, fraction = ''] = value.split('.');
  assert(!/[1-9]/.test(fraction.slice(6)), 'Test bills must be exactly representable in 6 decimals; no silent rounding');
  return BigInt(whole) * 1_000_000n + BigInt(fraction.slice(0, 6).padEnd(6, '0'));
}

export function billFromFixture(fixture, deployment, wallet) {
  assert(fixture.version === 1 && fixture.mode === 'isolated-gateway-fixture', 'Expected an isolated test fixture');
  assert(fixture.chainId === 10143 && deployment.chainId === 10143, 'Only Monad Testnet is supported');
  assert(/^0x[\da-f]{40}$/i.test(wallet) && fixture.wallet.toLowerCase() === wallet.toLowerCase(), 'Test wallet mismatch');
  assert(/^0x[\da-f]{40}$/i.test(deployment.paygBatch) && /^0x[\da-f]{40}$/i.test(deployment.kUSDC) && /^0x[\da-f]{40}$/i.test(deployment.provider), 'Invalid deployment');
  assert(deployment.paymentMode === 'token-allowance' && deployment.settlementMode === 'bill-total' && deployment.abiVersions?.TokenSwoopPayGBatch === 5, 'Expected current Batch deployment');
  assert(Array.isArray(fixture.requests) && fixture.requests.length === 4, 'Expected four metered requests');
  const seen = new Set();
  let total = 0n;
  for (const request of fixture.requests) {
    assert(/^[\da-f-]{36}$/i.test(request.requestId) && !seen.has(request.requestId.toLowerCase()), 'Duplicate or invalid request ID');
    seen.add(request.requestId.toLowerCase());
    assert(request.state === 'completed' && request.inputTokens === 20 && request.outputTokens === 5, 'Incomplete or unexpected fixture usage');
    assert(request.priceSnapshot?.price_version > 0 && request.usageSource, 'Missing price snapshot or usage provenance');
    const amount = units(request.amount);
    assert(amount === 30n, 'Each fixture request must cost exactly 30 micro kUSDC');
    total += amount;
  }
  assert(total === units(fixture.totalAmount) && total.toString() === fixture.totalUnits && total === 120n, 'Bill does not reconcile to 120 micro kUSDC');
  const fixtureHash = createHash('sha256').update(JSON.stringify(fixture)).digest('hex');
  // Hash includes chain, exact deployment, wallet and immutable request details.
  const domain = ['tokenswoop:testnet-gateway-fixture:v1', '10143', deployment.paygBatch.toLowerCase(), deployment.kUSDC.toLowerCase(), deployment.provider.toLowerCase(), wallet.toLowerCase(), fixtureHash];
  if(deployment.recipient) domain.push(deployment.recipient.toLowerCase());
  return {recipient:(deployment.recipient||deployment.provider).toLowerCase(),chainId: 10143, contract: deployment.paygBatch.toLowerCase(), token: deployment.kUSDC.toLowerCase(), provider: deployment.provider.toLowerCase(), user: wallet.toLowerCase(),
    billId: '0x' + createHash('sha256').update(domain.join(':')).digest('hex'), amountUnits: total.toString(), amount: '0.000120', fixtureHash};
}

export function addressTopic(address) {
  assert(/^0x[\da-f]{40}$/i.test(address), 'Invalid EVM address');
  return '0x' + address.slice(2).toLowerCase().padStart(64, '0');
}

export function verifyPayment(receipt, bill, topics) {
  assert(BigInt(receipt.status) === 1n, 'Payment reverted');
  const payments = receipt.logs.filter(log => log.address.toLowerCase() === bill.contract && log.topics[0] === topics.payment);
  const transfers = receipt.logs.filter(log => log.address.toLowerCase() === bill.token && log.topics[0] === topics.transfer);
  assert(payments.length === 1 && transfers.length === 1, 'Expected one payment and one token transfer');
  const [payment, transfer] = [payments[0], transfers[0]];
  for (const log of [payment, transfer]) {
    assert(log.topics[1] === addressTopic(bill.user) && log.topics[2] === addressTopic(log === payment ? bill.provider : (bill.recipient || bill.provider)), 'Wrong payer or recipient');
    assert(BigInt(log.data).toString() === bill.amountUnits, 'Incorrect onchain amount');
  }
  assert(payment.topics[3] === bill.billId, 'Wrong bill ID');
}
