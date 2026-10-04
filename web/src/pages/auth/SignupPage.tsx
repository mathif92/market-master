import { useState, type FormEvent } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { useAuth } from '@/lib/auth'
import { setStoredSlug, slugFromHost } from '@/lib/tenant'
import { request, ApiError, newIdempotencyKey } from '@/lib/api'
import { Button } from '@/components/ui/button'
import { Input, Field } from '@/components/ui/input'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { ErrorNote } from '@/components/ui/feedback'

const SLUG_RE = /^[a-z0-9][a-z0-9-]{1,38}[a-z0-9]$/

function marketOrigin(slug: string): string {
  const { protocol, port } = window.location
  return `${protocol}//${slug}.localhost${port ? `:${port}` : ''}`
}

export function SignupPage() {
  const { login } = useAuth()
  const navigate = useNavigate()
  const hostSlug = slugFromHost()

  const [slug, setSlug] = useState('')
  const [name, setName] = useState('')
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  const slugError =
    slug && !SLUG_RE.test(slug)
      ? '3–40 chars: lowercase letters, numbers, hyphens (no leading/trailing hyphen).'
      : ''

  async function onSubmit(e: FormEvent) {
    e.preventDefault()
    setError('')
    if (!SLUG_RE.test(slug)) {
      setError('Please choose a valid market URL.')
      return
    }
    setBusy(true)
    try {
      await request('/v1/tenants', {
        method: 'POST',
        anonymous: true,
        idempotencyKey: newIdempotencyKey(),
        body: {
          slug,
          name,
          admin: { email, password },
        },
      })
      if (!hostSlug) setStoredSlug(slug)
      await login(hostSlug || slug, email, password)
      navigate('/admin', { replace: true })
    } catch (err) {
      if (err instanceof ApiError) {
        const msg =
          err.code === 'slug_taken'
            ? 'This market URL is already taken — pick another one.'
            : err.code === 'email_taken'
              ? 'This email already has an account — sign in instead.'
              : err.message
        setError(msg)
      } else {
        setError('Could not create your market. Please try again.')
      }
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="mx-auto max-w-md py-8">
      <Card>
        <CardHeader>
          <CardTitle>Create your market</CardTitle>
          <CardDescription>
            Start selling on the platform: your own storefront, catalog, orders and logistics —
            no code required.
          </CardDescription>
        </CardHeader>
        <CardContent>
          <form onSubmit={onSubmit} className="space-y-4">
            <Field
              label="Market URL"
              htmlFor="slug"
              hint={
                slug && !slugError
                  ? `Your shop will live at ${marketOrigin(slug)}`
                  : 'Lowercase letters, numbers and hyphens.'
              }
            >
              <Input
                id="slug"
                required
                value={slug}
                onChange={(e) => setSlug(e.target.value.toLowerCase().trim())}
                placeholder="e.g. freshmart"
                autoComplete="off"
                spellCheck={false}
                aria-invalid={!!slugError}
              />
            </Field>
            {slugError && <ErrorNote message={slugError} />}
            <Field label="Market name" htmlFor="name">
              <Input
                id="name"
                required
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder="e.g. FreshMart"
                autoComplete="organization"
              />
            </Field>
            <Field label="Your email" htmlFor="email" hint="You'll be the market admin.">
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
            <Field label="Password" htmlFor="password" hint="At least 8 characters.">
              <Input
                id="password"
                type="password"
                required
                minLength={8}
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                autoComplete="new-password"
                placeholder="••••••••"
              />
            </Field>
            {error && <ErrorNote message={error} />}
            <Button type="submit" className="w-full" disabled={busy}>
              {busy ? 'Creating your market…' : 'Create market'}
            </Button>
            <p className="text-center text-sm text-slate-500">
              Already have an account?{' '}
              <Link to="/login" className="font-medium text-brand-600 hover:underline">
                Sign in
              </Link>
            </p>
          </form>
        </CardContent>
      </Card>
    </div>
  )
}
