import test from 'node:test'
import assert from 'node:assert/strict'
import { parseAmount, formatAmount } from '../src/amounts.ts'

test('six-decimal quantities retain exact precision, including values above Number.MAX_SAFE_INTEGER', () => {
  assert.equal(parseAmount('0.000001'), 1n)
  assert.equal(parseAmount('0.0042'), 4200n)
  assert.equal(parseAmount('9007199254740993.000001'), 9007199254740993000001n)
  assert.equal(formatAmount(9007199254740993000001n), '9007199254740993.000001')
})
test('invalid, rounded, zero and overflowing inputs cannot create transactions', () => {
  for (const value of ['', '0', '0.000000', '-1', '1e6', '0.0000001', '1,000', 'Infinity', ' 10 ', (2n ** 256n).toString()]) {
    assert.throws(() => parseAmount(value), value)
  }
})
test('balance formatting never uses floating-point rounding', () => {
  assert.equal(formatAmount(0n), '0')
  assert.equal(formatAmount(1n), '0.000001')
  assert.equal(formatAmount(1234500000n, true), '1,234.5')
})
