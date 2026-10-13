import { test } from 'node:test';
import assert from 'node:assert/strict';
import { units, billFromFixture, addressTopic, verifyPayment } from './lib/testnet-bill.mjs';

const wallet = '0x' + '11'.repeat(20);
const deployment = {chainId:10143, paygBatch:'0x'+'22'.repeat(20), kUSDC:'0x'+'33'.repeat(20), provider:'0x'+'44'.repeat(20), paymentMode:'token-allowance', settlementMode:'bill-total', abiVersions:{TokenSwoopPayGBatch:5}};
const fixture = () => ({version:1, mode:'isolated-gateway-fixture', chainId:10143, wallet, totalAmount:'0.00012', totalUnits:'120', requests:Array.from({length:4}, (_, i) => ({requestId:`00000000-0000-4000-8000-${String(i).padStart(12,'0')}`, state:'completed', amount:'0.000030000000000000', inputTokens:20, outputTokens:5, priceSnapshot:{price_version:1}, usageSource:'upstream'}))});

test('token conversion preserves precision and refuses rounding or floats', () => {
  assert.equal(units('0.000000000000000000'), 0n);
  assert.equal(units('0.000030000000000000'), 30n);
  assert.equal(units('12345678901234567890.123456'), 12345678901234567890123456n);
  for (const invalid of [0.00003, '-1', '1e-6', 'NaN', '0.0000001', '1.000001000000000001']) assert.throws(() => units(invalid));
});
test('stable bill ID binds wallet, deployment and request details', () => {
  const input = fixture();
  const a = billFromFixture(input, deployment, wallet);
  assert.deepEqual(a, billFromFixture(structuredClone(input), deployment, wallet));
  const other = {...deployment, paygBatch:'0x'+'55'.repeat(20)};
  assert.notEqual(a.billId, billFromFixture(input, other, wallet).billId);
  input.requests[0].priceSnapshot.price_version = 2;
  assert.notEqual(a.billId, billFromFixture(input, deployment, wallet).billId);
});
test('non-testnet, duplicate, uncertain and mismatched bills cannot be paid', () => {
  assert.throws(() => billFromFixture(fixture(), {...deployment, chainId:143}, wallet));
  for (const mutate of [f => f.requests.push(f.requests[0]), f => f.requests[1].requestId = f.requests[0].requestId, f => f.requests[0].state = 'usage_unknown', f => f.totalAmount = '1', f => f.requests[0].amount = '0.0000301', f => f.wallet = deployment.provider]) {
    const input = fixture(); mutate(input);
    assert.throws(() => billFromFixture(input, deployment, wallet));
  }
});
test('receipt verifies actual token movement as well as matching bill event', () => {
  const bill = billFromFixture(fixture(), deployment, wallet);
  const topics = {payment:'0x'+'66'.repeat(32), transfer:'0x'+'77'.repeat(32)};
  const receipt = {status:'0x1', logs:[{address:bill.contract, topics:[topics.payment, addressTopic(wallet), addressTopic(bill.provider), bill.billId], data:'0x78'}, {address:bill.token, topics:[topics.transfer, addressTopic(wallet), addressTopic(bill.provider)], data:'0x78'}]};
  verifyPayment(receipt, bill, topics);
  for (const mutate of [r => r.status = '0x0', r => r.logs.pop(), r => r.logs[1].data = '0x79', r => r.logs[0].topics[3] = topics.payment, r => r.logs[1].topics[2] = addressTopic(wallet)]) {
    const changed = structuredClone(receipt); mutate(changed);
    assert.throws(() => verifyPayment(changed, bill, topics));
  }
});

test('independent recipient cannot be confused with the executing provider', () => {
 const recipient='0x'+'88'.repeat(20);
 const bill=billFromFixture(fixture(),{...deployment,recipient},wallet);
 const topics={payment:'0x'+'66'.repeat(32),transfer:'0x'+'77'.repeat(32)};
 const receipt={status:'0x1',logs:[{address:bill.contract,topics:[topics.payment,addressTopic(wallet),addressTopic(bill.provider),bill.billId],data:'0x78'},{address:bill.token,topics:[topics.transfer,addressTopic(wallet),addressTopic(recipient)],data:'0x78'}]};
 verifyPayment(receipt,bill,topics);
 receipt.logs[1].topics[2]=addressTopic(bill.provider);
 assert.throws(()=>verifyPayment(receipt,bill,topics));
});
