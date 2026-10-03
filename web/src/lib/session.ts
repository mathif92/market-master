const STORE = 'market.auth.v1'

export interface Session {
  accessToken: string
  refreshToken: string
  user: import('./types').User
  /** epoch ms when the access token expires */
  expiresAt: number
}

export function loadSession(): Session | null {
  try {
    const raw = localStorage.getItem(STORE)
    if (!raw) return null
    const s = JSON.parse(raw) as Session
    if (!s.accessToken || !s.refreshToken || !s.user) return null
    return s
  } catch {
    return null
  }
}

export function saveSession(s: Session): void {
  localStorage.setItem(STORE, JSON.stringify(s))
}

export function clearSession(): void {
  localStorage.removeItem(STORE)
}
