import { createContext, useCallback, useContext, useEffect, useMemo, useState } from 'react'
import { request, onSessionLost } from './api'
import { loadSession, saveSession, clearSession, type Session } from './session'
import { activeSlug } from './tenant'
import type { Role, TokenResponse, User } from './types'

interface AuthState {
  user: User | null
  loading: boolean
  login(tenantSlug: string, email: string, password: string): Promise<User>
  register(email: string, password: string): Promise<User>
  logout(): void
  isStaff: boolean
  isPlatformAdmin: boolean
}

const AuthContext = createContext<AuthState | null>(null)

export function AuthProvider({ children }: { children: React.ReactNode }) {
  const [session, setSession] = useState<Session | null>(() => loadSession())
  const [refreshing, setRefreshing] = useState(() => {
    const s = loadSession()
    return !!s && s.expiresAt < Date.now() // validate in background if expired
  })

  useEffect(() => {
    const off = onSessionLost(() => setSession(null))
    return off
  }, [])

  useEffect(() => {
    if (session && session.expiresAt < Date.now()) {
      // fire-and-forget refresh; api.request also refreshes lazily
      void request('/v1/auth/refresh', {
        method: 'POST',
        anonymous: true,
        body: { refresh_token: session.refreshToken },
      })
        .then((t) => {
          const tok = t as TokenResponse
          const next: Session = {
            accessToken: tok.access_token,
            refreshToken: tok.refresh_token,
            user: tok.user,
            expiresAt: Date.now() + tok.expires_in * 1000,
          }
          saveSession(next)
          setSession(next)
        })
        .catch(() => {
          clearSession()
          setSession(null)
        })
        .finally(() => setRefreshing(false))
    }
  }, [session])

  const login = useCallback(async (tenantSlug: string, email: string, password: string) => {
    const t = await request<TokenResponse>('/v1/auth/login', {
      method: 'POST',
      anonymous: true,
      body: { tenant_slug: tenantSlug, email, password },
    })
    const next: Session = {
      accessToken: t.access_token,
      refreshToken: t.refresh_token,
      user: t.user,
      expiresAt: Date.now() + t.expires_in * 1000,
    }
    saveSession(next)
    setSession(next)
    return t.user
  }, [])

  const register = useCallback(async (email: string, password: string) => {
    await request('/v1/auth/register', { method: 'POST', body: { email, password } })
    // registration doesn't log in automatically — sign in right after
    return login(activeSlug(), email, password)
  }, [login])

  const logout = useCallback(() => {
    clearSession()
    setSession(null)
  }, [])

  const value = useMemo<AuthState>(
    () => ({
      user: session?.user ?? null,
      loading: refreshing,
      login,
      register,
      logout,
      isStaff: isStaffRole(session?.user?.role),
      isPlatformAdmin: session?.user?.role === 'platform_admin',
    }),
    [session, refreshing, login, register, logout],
  )

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>
}

export function isStaffRole(role: Role | undefined): boolean {
  return role === 'tenant_admin' || role === 'staff' || role === 'platform_admin'
}

export function useAuth(): AuthState {
  const ctx = useContext(AuthContext)
  if (!ctx) throw new Error('useAuth must be used inside <AuthProvider>')
  return ctx
}
