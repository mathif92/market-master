import { useState, type FormEvent } from 'react'
import { Link, useLocation, useNavigate } from 'react-router-dom'
import { useAuth } from '@/lib/auth'
import { activeSlug, slugFromHost, setStoredSlug } from '@/lib/tenant'
import { ApiError } from '@/lib/api'
import { Button } from '@/components/ui/button'
import { Input, Field } from '@/components/ui/input'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { ErrorNote } from '@/components/ui/feedback'

export function LoginPage() {
  const { login } = useAuth()
  const navigate = useNavigate()
  const location = useLocation()
  const hostSlug = slugFromHost()

  const [slug, setSlug] = useState(activeSlug())
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  async function onSubmit(e: FormEvent) {
    e.preventDefault()
    setError('')
    setBusy(true)
    try {
      if (!hostSlug) setStoredSlug(slug.trim()) // clearing the field clears the override
      const u = await login(hostSlug || slug.trim(), email, password)
      const from = (location.state as { from?: string } | null)?.from
      const fallback = u.role === 'platform_admin' ? '/platform' : '/'
      navigate(from && from !== '/login' ? from : fallback, { replace: true })
    } catch (err) {
      setError(err instanceof ApiError ? err.message : 'Sign in failed')
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="mx-auto max-w-md py-8">
      <Card>
        <CardHeader>
          <CardTitle>Sign in</CardTitle>
          <CardDescription>
            Access your account{hostSlug ? ` on ${hostSlug}` : ' for this market'}.
          </CardDescription>
        </CardHeader>
        <CardContent>
          <form onSubmit={onSubmit} className="space-y-4">
            {!hostSlug && (
              <Field label="Market" htmlFor="slug" hint="Leave blank if browsing localhost without a market.">
                <Input
                  id="slug"
                  value={slug}
                  onChange={(e) => setSlug(e.target.value)}
                  placeholder="e.g. demo"
                  autoComplete="organization"
                />
              </Field>
            )}
            <Field label="Email" htmlFor="email">
              <Input
                id="email"
                type="email"
                required
                value={email}
                onChange={(e) => setEmail(e.target.value)}
                autoComplete="email"
                placeholder="you@example.com"
              />
            </Field>
            <Field label="Password" htmlFor="password">
              <Input
                id="password"
                type="password"
                required
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                autoComplete="current-password"
                placeholder="••••••••"
              />
            </Field>
            {error && <ErrorNote message={error} />}
            <Button type="submit" className="w-full" disabled={busy}>
              {busy ? 'Signing in…' : 'Sign in'}
            </Button>
            <p className="text-center text-sm text-slate-500">
              No account?{' '}
              <Link to="/register" className="font-medium text-brand-600 hover:underline">
                Create one
              </Link>
            </p>
            <p className="text-center text-sm text-slate-500">
              Want to sell on the platform?{' '}
              <Link to="/signup" className="font-medium text-brand-600 hover:underline">
                Create your market
              </Link>
            </p>
          </form>
        </CardContent>
      </Card>
    </div>
  )
}
