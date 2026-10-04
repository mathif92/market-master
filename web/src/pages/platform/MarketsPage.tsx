import { useState, type FormEvent } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { ExternalLink, Plus } from 'lucide-react'
import { request, ApiError, newIdempotencyKey } from '@/lib/api'
import type { Tenant } from '@/lib/types'
import { Button } from '@/components/ui/button'
import { Input, Field } from '@/components/ui/input'
import { Card, CardContent } from '@/components/ui/card'
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Badge } from '@/components/ui/badge'
import { Table, TBody, TD, TH, THead, TR } from '@/components/ui/table'
import { PageLoading, ErrorNote, EmptyState, Spinner } from '@/components/ui/feedback'

const SLUG_RE = /^[a-z0-9][a-z0-9-]{1,38}[a-z0-9]$/

function marketOrigin(slug: string): string {
  const { protocol, port } = window.location
  return `${protocol}//${slug}.localhost${port ? `:${port}` : ''}`
}

function fmtDate(iso: string): string {
  return new Date(iso).toLocaleDateString(undefined, {
    year: 'numeric',
    month: 'short',
    day: 'numeric',
  })
}

export function MarketsPage() {
  const qc = useQueryClient()

  const { data, isLoading, error } = useQuery({
    queryKey: ['platform', 'tenants'],
    queryFn: () => request<{ tenants: Tenant[] }>('/v1/tenants'),
  })

  const [open, setOpen] = useState(false)
  const [form, setForm] = useState({ slug: '', name: '', email: '', password: '' })
  const [formError, setFormError] = useState('')

  const create = useMutation({
    mutationFn: () =>
      request<{ tenant: Tenant }>('/v1/tenants', {
        method: 'POST',
        idempotencyKey: newIdempotencyKey(),
        body: {
          slug: form.slug,
          name: form.name,
          admin: { email: form.email, password: form.password },
        },
      }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['platform', 'tenants'] })
      setOpen(false)
      setForm({ slug: '', name: '', email: '', password: '' })
      setFormError('')
    },
    onError: (err) => {
      if (err instanceof ApiError) {
        setFormError(
          err.code === 'slug_taken'
            ? 'This market URL is already taken.'
            : err.code === 'email_taken'
              ? 'This admin email already has an account.'
              : err.message,
        )
      } else {
        setFormError('Create failed')
      }
    },
  })

  const setStatus = useMutation({
    mutationFn: ({ id, status }: { id: string; status: 'active' | 'suspended' }) =>
      request<Tenant>(`/v1/tenants/${id}`, { method: 'PATCH', body: { status } }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['platform', 'tenants'] }),
  })

  if (isLoading) return <PageLoading />
  if (error) return <ErrorNote message="Could not load markets." />

  const tenants = data?.tenants ?? []

  return (
    <div className="space-y-6">
      <div className="flex items-end justify-between">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">Markets</h1>
          <p className="text-sm text-slate-500">
            Every tenant on the platform — suspend a market to take its storefront offline.
          </p>
        </div>
        <Button onClick={() => setOpen(true)}>
          <Plus className="size-4" /> New market
        </Button>
      </div>

      {tenants.length === 0 ? (
        <EmptyState title="No markets yet" description="Create the first one to get started." />
      ) : (
        <Card>
          <CardContent className="p-0">
            <Table>
              <THead>
                <tr>
                  <TH>Market</TH>
                  <TH>Status</TH>
                  <TH>Created</TH>
                  <TH className="text-right">Actions</TH>
                </tr>
              </THead>
              <TBody>
                {tenants.map((t) => (
                  <TR key={t.id}>
                    <TD>
                      <div className="font-medium text-slate-900">{t.name}</div>
                      <div className="text-xs text-slate-500">{t.slug}</div>
                    </TD>
                    <TD>
                      <Badge variant={t.status === 'active' ? 'success' : 'danger'}>
                        {t.status}
                      </Badge>
                    </TD>
                    <TD className="text-slate-500">{fmtDate(t.created_at)}</TD>
                    <TD>
                      <div className="flex items-center justify-end gap-2">
                        <Button variant="outline" size="sm" asChild>
                          <a href={marketOrigin(t.slug)} target="_blank" rel="noreferrer">
                            <ExternalLink className="size-3.5" /> Open
                          </a>
                        </Button>
                        <Button
                          variant={t.status === 'active' ? 'destructive' : 'default'}
                          size="sm"
                          disabled={setStatus.isPending}
                          onClick={() =>
                            setStatus.mutate({
                              id: t.id,
                              status: t.status === 'active' ? 'suspended' : 'active',
                            })
                          }
                        >
                          {setStatus.isPending ? <Spinner /> : null}
                          {t.status === 'active' ? 'Suspend' : 'Activate'}
                        </Button>
                      </div>
                    </TD>
                  </TR>
                ))}
              </TBody>
            </Table>
          </CardContent>
        </Card>
      )}

      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>New market</DialogTitle>
          </DialogHeader>
          <form
            className="space-y-4"
            onSubmit={(e: FormEvent) => {
              e.preventDefault()
              if (!SLUG_RE.test(form.slug)) {
                setFormError('Market URL must be 3–40 chars of a-z, 0-9, hyphen.')
                return
              }
              create.mutate()
            }}
          >
            <Field
              label="Market URL"
              htmlFor="m-slug"
              hint={
                form.slug && SLUG_RE.test(form.slug)
                  ? `Storefront at ${marketOrigin(form.slug)}`
                  : 'Lowercase letters, numbers and hyphens.'
              }
            >
              <Input
                id="m-slug"
                required
                value={form.slug}
                onChange={(e) => setForm({ ...form, slug: e.target.value.toLowerCase().trim() })}
                placeholder="freshmart"
                autoComplete="off"
                spellCheck={false}
              />
            </Field>
            <Field label="Market name" htmlFor="m-name">
              <Input
                id="m-name"
                required
                value={form.name}
                onChange={(e) => setForm({ ...form, name: e.target.value })}
                placeholder="FreshMart"
              />
            </Field>
            <Field label="Admin email" htmlFor="m-email" hint="First market admin account.">
              <Input
                id="m-email"
                type="email"
                required
                value={form.email}
                onChange={(e) => setForm({ ...form, email: e.target.value })}
                placeholder="admin@freshmart.test"
              />
            </Field>
            <Field label="Admin password" htmlFor="m-pass" hint="At least 8 characters.">
              <Input
                id="m-pass"
                type="password"
                required
                minLength={8}
                value={form.password}
                onChange={(e) => setForm({ ...form, password: e.target.value })}
                autoComplete="new-password"
              />
            </Field>
            {formError && <ErrorNote message={formError} />}
            <DialogFooter>
              <Button type="button" variant="outline" onClick={() => setOpen(false)}>
                Cancel
              </Button>
              <Button type="submit" disabled={create.isPending}>
                {create.isPending && <Spinner />} Create
              </Button>
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>
    </div>
  )
}
