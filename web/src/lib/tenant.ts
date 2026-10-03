// Market (tenant) resolution: host subdomain wins, X-Tenant-Slug is the
// localhost/dev fallback — mirrors the gateway's own resolution order.

const OVERRIDE = 'market.slug'

/** First label of the hostname when it looks like a market subdomain. */
export function slugFromHost(): string {
  const host = window.location.hostname.toLowerCase()
  if (!host || host === 'localhost' || host === '127.0.0.1' || /^\d+$/.test(host.replace(/\./g, ''))) {
    return ''
  }
  const first = host.split('.')[0]
  return first && first !== 'www' ? first : ''
}

export function storedSlug(): string {
  return localStorage.getItem(OVERRIDE) ?? ''
}

export function setStoredSlug(slug: string): void {
  if (slug) localStorage.setItem(OVERRIDE, slug)
  else localStorage.removeItem(OVERRIDE)
}

/** Slug actually used for API calls: host first, stored override second. */
export function activeSlug(): string {
  return slugFromHost() || storedSlug()
}
