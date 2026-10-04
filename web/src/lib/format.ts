import type { Product } from './types'

export function formatMoney(cents: number, currency = 'USD'): string {
  return new Intl.NumberFormat('en-US', { style: 'currency', currency }).format(cents / 100)
}

export function formatDate(iso: string): string {
  return new Date(iso).toLocaleString(undefined, {
    month: 'short',
    day: 'numeric',
    year: 'numeric',
    hour: 'numeric',
    minute: '2-digit',
  })
}

export function shortId(id: string): string {
  return id.slice(0, 8)
}

/** What the customer pays: the live sale price when a campaign applies. */
export function effectivePrice(p: Pick<Product, 'price_cents' | 'sale_price_cents'>): number {
  return p.sale_price_cents ?? p.price_cents
}

/** "50% off" / "$5.00 off" label for a campaign rule. */
export function formatRule(ruleType: 'percent' | 'fixed', ruleValue: number, currency = 'USD'): string {
  return ruleType === 'percent'
    ? `${ruleValue / 100}% off`
    : `${formatMoney(ruleValue, currency)} off`
}
