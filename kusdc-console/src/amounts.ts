export function parseAmount(value: string): bigint {
  if (!/^\d+(\.\d{1,6})?$/.test(value)) {
    throw new Error('请输入正数，最多 6 位小数，不支持科学计数法。')
  }
  const [whole, fraction = ''] = value.split('.')
  const units = BigInt(whole) * 1_000_000n + BigInt(fraction.padEnd(6, '0'))
  if (units <= 0n || units >= 2n ** 256n) {
    throw new Error('数量必须大于 0，且在合约允许的范围内。')
  }
  return units
}

export function formatAmount(value: bigint, grouped = false): string {
  const digits = value.toString().padStart(7, '0')
  const whole = digits.slice(0, -6)
  const fraction = digits.slice(-6).replace(/0+$/, '')
  return `${grouped ? whole.replace(/\B(?=(\d{3})+(?!\d))/g, ',') : whole}${fraction ? '.' + fraction : ''}`
}
