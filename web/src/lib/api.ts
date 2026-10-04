import { activeSlug } from './tenant'
import { clearSession, loadSession, saveSession } from './session'
import type { ApiErrorBody, TokenResponse } from './types'

export class ApiError extends Error {
  status: number
  code: string

  constructor(status: number, code: string, message: string) {
    super(message)
    this.status = status
    this.code = code
  }
}

const listeners = new Set<() => void>()

/** Notified whenever the session is cleared (expired/revoked token). */
export function onSessionLost(fn: () => void): () => void {
  listeners.add(fn)
  return () => listeners.delete(fn)
}

function sessionLost(): void {
  clearSession()
  for (const fn of listeners) fn()
}

export interface RequestOptions {
  method?: 'GET' | 'POST' | 'PATCH' | 'PUT' | 'DELETE'
  body?: unknown
  /** Sent as Idempotency-Key; generated for mutating calls when omitted. */
  idempotencyKey?: string
  /** Skip the Authorization header (public endpoints). */
  anonymous?: boolean
  signal?: AbortSignal
}

export function newIdempotencyKey(): string {
  return crypto.randomUUID()
}

async function rawFetch(path: string, opts: RequestOptions): Promise<Response> {
  const method = opts.method ?? 'GET'
  const headers: Record<string, string> = { Accept: 'application/json' }
  if (opts.body !== undefined) headers['Content-Type'] = 'application/json'

  const slug = activeSlug()
  if (slug) headers['X-Tenant-Slug'] = slug

  if (!opts.anonymous) {
    const s = loadSession()
    if (s) headers.Authorization = `Bearer ${s.accessToken}`
  }
  if (method !== 'GET' && opts.idempotencyKey) {
    headers['Idempotency-Key'] = opts.idempotencyKey
  }

  return fetch(path, {
    method,
    headers,
    body: opts.body !== undefined ? JSON.stringify(opts.body) : undefined,
    signal: opts.signal,
  })
}

async function tryRefresh(): Promise<boolean> {
  const s = loadSession()
  if (!s) return false
  const res = await fetch('/v1/auth/refresh', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ refresh_token: s.refreshToken }),
  }).catch(() => null)
  if (!res || !res.ok) return false
  const t = (await res.json()) as TokenResponse
  saveSession({
    accessToken: t.access_token,
    refreshToken: t.refresh_token,
    user: t.user,
    expiresAt: Date.now() + t.expires_in * 1000,
  })
  return true
}

/**
 * request<T> — single entry point for the API.
 *
 * - attaches market slug + bearer token
 * - transparently refreshes an expired access token once and retries
 * - maps `{"error":{code,message}}` responses to ApiError
 */
export async function request<T>(path: string, opts: RequestOptions = {}): Promise<T> {
  let res = await rawFetch(path, opts)

  if (res.status === 401 && !opts.anonymous) {
    if (await tryRefresh()) {
      res = await rawFetch(path, opts)
    } else {
      sessionLost()
      throw new ApiError(401, 'unauthorized', 'session expired, please sign in again')
    }
  }

  if (res.status === 204) return undefined as T

  const text = await res.text()
  const json: unknown = text ? JSON.parse(text) : null

  if (!res.ok) {
    const body = json as ApiErrorBody | null
    const code = body?.error?.code ?? 'error'
    const message = body?.error?.message ?? `request failed (${res.status})`
    throw new ApiError(res.status, code, message)
  }
  return json as T
}

/**
 * uploadFile — multipart POST (stock CSV/XLSX ingest). Same auth/session
 * handling as request(), but the body is FormData.
 */
export async function uploadFile<T>(path: string, form: FormData): Promise<T> {
  const headers: Record<string, string> = { Accept: 'application/json' }
  const slug = activeSlug()
  if (slug) headers['X-Tenant-Slug'] = slug
  const auth = () => {
    const s = loadSession()
    return s ? `Bearer ${s.accessToken}` : ''
  }

  const send = () => fetch(path, { method: 'POST', headers: { ...headers, Authorization: auth() }, body: form })
  let res = await send()

  if (res.status === 401) {
    if (await tryRefresh()) {
      res = await send()
    } else {
      sessionLost()
      throw new ApiError(401, 'unauthorized', 'session expired, please sign in again')
    }
  }

  const text = await res.text()
  const json: unknown = text ? JSON.parse(text) : null
  if (!res.ok) {
    const body = json as ApiErrorBody | null
    throw new ApiError(
      res.status,
      body?.error?.code ?? 'error',
      body?.error?.message ?? `upload failed (${res.status})`,
    )
  }
  return json as T
}
