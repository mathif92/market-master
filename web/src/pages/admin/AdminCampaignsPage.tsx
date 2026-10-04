import { useState, type FormEvent } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { request, newIdempotencyKey, ApiError } from '@/lib/api'
import type { Campaign, CampaignCode, Category, Product } from '@/lib/types'
import { formatDate, formatRule, shortId } from '@/lib/format'
import { Button } from '@/components/ui/button'
import { Input, Field } from '@/components/ui/input'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Table, TBody, TD, TH, THead, TR } from '@/components/ui/table'
import { Badge, StatusBadge } from '@/components/ui/badge'
import { PageLoading, ErrorNote, Spinner } from '@/components/ui/feedback'
import { Key, Pencil, Plus, Trash2 } from 'lucide-react'

type EditTarget = Campaign | 'new' | null

export function AdminCampaignsPage() {
  const qc = useQueryClient()
  const [editing, setEditing] = useState<EditTarget>(null)
  const [codesFor, setCodesFor] = useState<Campaign | null>(null)

  const campaigns = useQuery({
    queryKey: ['campaigns'],
    queryFn: () => request<{ campaigns: Campaign[] }>('/v1/campaigns'),
  })

  const archive = useMutation({
    mutationFn: (id: string) => request<void>(`/v1/campaigns/${id}`, { method: 'DELETE' }),
    onSettled: () => {
      qc.invalidateQueries({ queryKey: ['campaigns'] })
      qc.invalidateQueries({ queryKey: ['active-campaigns'] })
      qc.invalidateQueries({ queryKey: ['products'] })
    },
  })

  if (campaigns.isLoading) return <PageLoading />
  if (campaigns.error) return <ErrorNote message="Could not load campaigns." />

  const list = campaigns.data?.campaigns ?? []

  return (
    <div className="space-y-6">
      <div className="flex items-start justify-between gap-4">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">Campaigns</h1>
          <p className="text-sm text-slate-500">
            Time-windowed discounts — sitewide, category or product scope. Exactly one campaign
            applies per order (the biggest discount wins); coupon campaigns need a code at checkout.
          </p>
        </div>
        <Button onClick={() => setEditing('new')}>
          <Plus className="size-4" /> New campaign
        </Button>
      </div>

      <Card>
        <CardHeader>
          <CardTitle>All campaigns</CardTitle>
        </CardHeader>
        <CardContent className="p-0">
          <Table>
            <THead>
              <tr>
                <TH>Campaign</TH>
                <TH>Discount</TH>
                <TH>Scope</TH>
                <TH>Window</TH>
                <TH className="text-right">Redemptions</TH>
                <TH className="w-56 text-right">Actions</TH>
              </tr>
            </THead>
            <TBody>
              {list.length === 0 && (
                <TR>
                  <TD colSpan={6} className="py-8 text-center text-slate-500">
                    No campaigns yet — create one to run a sale.
                  </TD>
                </TR>
              )}
              {list.map((c) => (
                <TR key={c.id}>
                  <TD>
                    <div className="flex items-center gap-2">
                      <p className="font-medium">{c.name}</p>
                      <StatusBadge status={c.status} />
                      {c.requires_code && (
                        <Badge variant="info">
                          <Key className="mr-1 size-3" /> code
                        </Badge>
                      )}
                    </div>
                  </TD>
                  <TD className="font-medium text-red-600">
                    {formatRule(c.rule_type, c.rule_value)}
                  </TD>
                  <TD className="text-slate-600">
                    {c.scope_type}
                    {c.scope_id && (
                      <span className="ml-1 font-mono text-xs text-slate-400">
                        {shortId(c.scope_id)}
                      </span>
                    )}
                  </TD>
                  <TD className="text-xs text-slate-500">
                    {formatDate(c.starts_at)}
                    <br />
                    {formatDate(c.ends_at)}
                  </TD>
                  <TD className="text-right">
                    <span className="font-medium">{c.redemptions_count}</span>
                    <span className="text-slate-400">
                      {c.max_redemptions ? ` / ${c.max_redemptions}` : ''}
                    </span>
                  </TD>
                  <TD className="text-right">
                    <div className="flex justify-end gap-1.5">
                      <Button variant="outline" size="sm" onClick={() => setCodesFor(c)}>
                        <Key className="size-3.5" /> Codes
                      </Button>
                      <Button variant="outline" size="sm" onClick={() => setEditing(c)}>
                        <Pencil className="size-3.5" /> Edit
                      </Button>
                      {c.status !== 'archived' && (
                        <Button
                          variant="outline"
                          size="sm"
                          disabled={archive.isPending}
                          onClick={() => archive.mutate(c.id)}
                        >
                          <Trash2 className="size-3.5" /> Archive
                        </Button>
                      )}
                    </div>
                  </TD>
                </TR>
              ))}
            </TBody>
          </Table>
        </CardContent>
      </Card>

      {archive.error instanceof ApiError && <ErrorNote message={archive.error.message} />}

      <CampaignDialog target={editing} onClose={() => setEditing(null)} />
      <CodesDialog campaign={codesFor} onClose={() => setCodesFor(null)} />
    </div>
  )
}

/** ISO string → value for a datetime-local input (browser local time). */
function toLocalInput(iso: string): string {
  const d = new Date(iso)
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`
}

function CampaignDialog({ target, onClose }: { target: EditTarget; onClose: () => void }) {
  return (
    <Dialog open={!!target} onOpenChange={(v) => !v && onClose()}>
      <DialogContent>
        {target && (
          <CampaignForm
            key={target === 'new' ? 'new' : target.id}
            target={target}
            onClose={onClose}
          />
        )}
      </DialogContent>
    </Dialog>
  )
}

function CampaignForm({ target, onClose }: { target: Campaign | 'new'; onClose: () => void }) {
  const qc = useQueryClient()
  const isNew = target === 'new'
  const c = isNew ? null : target

  const cats = useQuery({
    queryKey: ['categories'],
    queryFn: () => request<{ categories: Category[] }>('/v1/categories'),
  })
  const prods = useQuery({
    queryKey: ['products-all'],
    queryFn: () => request<{ products: Product[] }>('/v1/products'),
  })

  const [name, setName] = useState(c?.name ?? '')
  const [status, setStatus] = useState(c?.status ?? 'draft')
  const [ruleType, setRuleType] = useState(c?.rule_type ?? 'percent')
  const [ruleValue, setRuleValue] = useState(String(c?.rule_value ?? 5000))
  const [scopeType, setScopeType] = useState(c?.scope_type ?? 'sitewide')
  const [scopeId, setScopeId] = useState(c?.scope_id ?? '')
  const [requiresCode, setRequiresCode] = useState(c?.requires_code ?? false)
  const [startsAt, setStartsAt] = useState(() =>
    toLocalInput(c?.starts_at ?? new Date().toISOString()),
  )
  const [endsAt, setEndsAt] = useState(() =>
    toLocalInput(c?.ends_at ?? new Date(Date.now() + 7 * 86400_000).toISOString()),
  )
  const [maxRedemptions, setMaxRedemptions] = useState(String(c?.max_redemptions ?? ''))
  const [error, setError] = useState('')
  const [key, setKey] = useState(() => newIdempotencyKey())

  const save = useMutation({
    mutationFn: () => {
      const body = {
        name: name.trim(),
        status,
        rule_type: ruleType,
        rule_value: Number(ruleValue),
        scope_type: scopeType,
        scope_id: scopeType === 'sitewide' ? '' : scopeId,
        requires_code: requiresCode,
        starts_at: new Date(startsAt).toISOString(),
        ends_at: new Date(endsAt).toISOString(),
        max_redemptions: maxRedemptions === '' ? null : Number(maxRedemptions),
      }
      return isNew
        ? request<Campaign>('/v1/campaigns', { method: 'POST', idempotencyKey: key, body })
        : request<Campaign>(`/v1/campaigns/${(c as Campaign).id}`, { method: 'PUT', body })
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['campaigns'] })
      qc.invalidateQueries({ queryKey: ['active-campaigns'] })
      qc.invalidateQueries({ queryKey: ['products'] })
      onClose()
    },
    onError: (err) => {
      // a recorded 4xx would replay forever with the same key
      setKey(newIdempotencyKey())
      setError(err instanceof ApiError ? err.message : 'Save failed')
    },
  })

  function onSubmit(e: FormEvent) {
    e.preventDefault()
    setError('')
    if (!name.trim()) return setError('Name is required.')
    const rv = Number(ruleValue)
    if (!Number.isInteger(rv) || rv < 1) return setError('Discount must be a positive whole number.')
    if (ruleType === 'percent' && rv > 10000) return setError('Percent discount max is 10000 basis points (100%).')
    if (scopeType !== 'sitewide' && !scopeId) return setError('Pick a category or product for this scope.')
    if (new Date(endsAt) <= new Date(startsAt)) return setError('End must be after start.')
    if (maxRedemptions !== '' && (!Number.isInteger(Number(maxRedemptions)) || Number(maxRedemptions) < 1)) {
      return setError('Max redemptions must be a positive whole number (or empty for unlimited).')
    }
    save.mutate()
  }

  const selectCls =
    'flex h-9 w-full rounded-lg border border-slate-300 bg-white px-3 text-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-brand-500'

  return (
    <form onSubmit={onSubmit} className="space-y-4">
      <DialogHeader>
        <DialogTitle>{isNew ? 'New campaign' : `Edit — ${c?.name}`}</DialogTitle>
      </DialogHeader>

      <Field label="Name" htmlFor="cmp-name">
        <Input id="cmp-name" required value={name} onChange={(e) => setName(e.target.value)} placeholder="Black Friday" />
      </Field>

      <div className="grid grid-cols-2 gap-4">
        <Field label="Status" htmlFor="cmp-status" hint="active = applies inside its window">
          <select
            id="cmp-status"
            className={selectCls}
            value={status}
            onChange={(e) => setStatus(e.target.value as Campaign['status'])}
          >
            <option value="draft">draft</option>
            <option value="active">active</option>
            {c?.status === 'archived' && <option value="archived">archived</option>}
          </select>
        </Field>
        <Field label="Discount type" htmlFor="cmp-rule">
          <select
            id="cmp-rule"
            className={selectCls}
            value={ruleType}
            onChange={(e) => setRuleType(e.target.value as Campaign['rule_type'])}
          >
            <option value="percent">percent</option>
            <option value="fixed">fixed (cents off per unit)</option>
          </select>
        </Field>
        <Field
          label={ruleType === 'percent' ? 'Percent (basis points)' : 'Cents off per unit'}
          htmlFor="cmp-value"
          hint={ruleType === 'percent' ? 'e.g. 5000 = 50% off' : 'e.g. 500 = $5.00 off each unit'}
        >
          <Input
            id="cmp-value"
            required
            inputMode="numeric"
            value={ruleValue}
            onChange={(e) => setRuleValue(e.target.value)}
          />
        </Field>
        <Field label="Max redemptions" htmlFor="cmp-max" hint="empty = unlimited">
          <Input
            id="cmp-max"
            inputMode="numeric"
            value={maxRedemptions}
            onChange={(e) => setMaxRedemptions(e.target.value)}
            placeholder="unlimited"
          />
        </Field>
      </div>

      <div className="grid grid-cols-2 gap-4">
        <Field label="Scope" htmlFor="cmp-scope">
          <select
            id="cmp-scope"
            className={selectCls}
            value={scopeType}
            onChange={(e) => {
              setScopeType(e.target.value as Campaign['scope_type'])
              setScopeId('')
            }}
          >
            <option value="sitewide">sitewide — every product</option>
            <option value="category">category</option>
            <option value="product">product</option>
          </select>
        </Field>
        {scopeType === 'category' && (
          <Field label="Category" htmlFor="cmp-scope-id">
            <select
              id="cmp-scope-id"
              className={selectCls}
              value={scopeId}
              onChange={(e) => setScopeId(e.target.value)}
            >
              <option value="">choose…</option>
              {(cats.data?.categories ?? []).map((cat) => (
                <option key={cat.id} value={cat.id}>
                  {cat.name}
                </option>
              ))}
            </select>
          </Field>
        )}
        {scopeType === 'product' && (
          <Field label="Product" htmlFor="cmp-scope-id">
            <select
              id="cmp-scope-id"
              className={selectCls}
              value={scopeId}
              onChange={(e) => setScopeId(e.target.value)}
            >
              <option value="">choose…</option>
              {(prods.data?.products ?? []).map((p) => (
                <option key={p.id} value={p.id}>
                  {p.name}
                </option>
              ))}
            </select>
          </Field>
        )}
      </div>

      <div className="grid grid-cols-2 gap-4">
        <Field label="Starts" htmlFor="cmp-starts">
          <Input
            id="cmp-starts"
            type="datetime-local"
            required
            value={startsAt}
            onChange={(e) => setStartsAt(e.target.value)}
          />
        </Field>
        <Field label="Ends" htmlFor="cmp-ends">
          <Input
            id="cmp-ends"
            type="datetime-local"
            required
            value={endsAt}
            onChange={(e) => setEndsAt(e.target.value)}
          />
        </Field>
      </div>

      <label className="flex items-center gap-2 text-sm text-slate-700">
        <input
          type="checkbox"
          checked={requiresCode}
          onChange={(e) => setRequiresCode(e.target.checked)}
          className="size-4 rounded border-slate-300"
        />
        Coupon code required (never shown as a public sale — customers must enter a code)
      </label>

      {error && <ErrorNote message={error} />}
      <DialogFooter>
        <Button type="button" variant="outline" onClick={onClose}>
          Cancel
        </Button>
        <Button type="submit" disabled={save.isPending}>
          {save.isPending && <Spinner />} {isNew ? 'Create' : 'Save'}
        </Button>
      </DialogFooter>
    </form>
  )
}

function CodesDialog({ campaign, onClose }: { campaign: Campaign | null; onClose: () => void }) {
  return (
    <Dialog open={!!campaign} onOpenChange={(v) => !v && onClose()}>
      <DialogContent>
        {campaign && <CodesPanel key={campaign.id} campaign={campaign} onClose={onClose} />}
      </DialogContent>
    </Dialog>
  )
}

function CodesPanel({ campaign, onClose }: { campaign: Campaign; onClose: () => void }) {
  const qc = useQueryClient()
  const [count, setCount] = useState('5')
  const [maxUses, setMaxUses] = useState('')
  const [error, setError] = useState('')
  const [mintKey, setMintKey] = useState(() => newIdempotencyKey())

  const codes = useQuery({
    queryKey: ['campaign-codes', campaign.id],
    queryFn: () => request<{ codes: CampaignCode[] }>(`/v1/campaigns/${campaign.id}/codes`),
  })

  const mint = useMutation({
    mutationFn: () =>
      request<{ codes: CampaignCode[] }>(`/v1/campaigns/${campaign.id}/codes`, {
        method: 'POST',
        idempotencyKey: mintKey,
        body: {
          count: Number(count),
          max_uses: maxUses === '' ? 0 : Number(maxUses),
        },
      }),
    onSuccess: () => {
      setError('')
      qc.invalidateQueries({ queryKey: ['campaign-codes', campaign.id] })
      qc.invalidateQueries({ queryKey: ['campaigns'] })
    },
    onError: (err) => {
      setMintKey(newIdempotencyKey())
      setError(err instanceof ApiError ? err.message : 'Could not generate codes')
    },
  })

  const remove = useMutation({
    mutationFn: (id: string) => request<void>(`/v1/campaigns/codes/${id}`, { method: 'DELETE' }),
    onSuccess: () => {
      setError('')
      qc.invalidateQueries({ queryKey: ['campaign-codes', campaign.id] })
    },
    onError: (err) => setError(err instanceof ApiError ? err.message : 'Delete failed'),
  })

  function onMint(e: FormEvent) {
    e.preventDefault()
    const n = Number(count)
    if (!Number.isInteger(n) || n < 1 || n > 100) {
      setError('Count must be 1–100.')
      return
    }
    if (maxUses !== '' && (!Number.isInteger(Number(maxUses)) || Number(maxUses) < 1)) {
      setError('Max uses must be a positive whole number (or empty for unlimited).')
      return
    }
    mint.mutate()
  }

  const list = codes.data?.codes ?? []

  return (
    <div className="space-y-4">
      <DialogHeader>
        <DialogTitle>
          Coupon codes — {campaign.name}{' '}
          {!campaign.requires_code && (
            <span className="font-normal text-slate-500">(campaign also auto-applies without a code)</span>
          )}
        </DialogTitle>
      </DialogHeader>

      <form onSubmit={onMint} className="flex flex-wrap items-end gap-3">
        <Field label="How many" htmlFor="code-count">
          <Input
            id="code-count"
            className="w-24"
            inputMode="numeric"
            value={count}
            onChange={(e) => setCount(e.target.value)}
          />
        </Field>
        <Field label="Max uses per code" htmlFor="code-max" hint="empty = unlimited">
          <Input
            id="code-max"
            className="w-32"
            inputMode="numeric"
            placeholder="unlimited"
            value={maxUses}
            onChange={(e) => setMaxUses(e.target.value)}
          />
        </Field>
        <Button type="submit" disabled={mint.isPending}>
          {mint.isPending && <Spinner />} Generate
        </Button>
      </form>

      <div className="max-h-72 overflow-y-auto rounded-lg border border-slate-200">
        <Table>
          <THead>
            <tr>
              <TH>Code</TH>
              <TH className="text-right">Uses</TH>
              <TH>Created</TH>
              <TH className="w-20" />
            </tr>
          </THead>
          <TBody>
            {list.length === 0 && (
              <TR>
                <TD colSpan={4} className="py-6 text-center text-slate-500">
                  No codes yet.
                </TD>
              </TR>
            )}
            {list.map((code) => (
              <TR key={code.id}>
                <TD className="font-mono text-sm font-medium">{code.code}</TD>
                <TD className="text-right">
                  {code.uses_count}
                  <span className="text-slate-400">{code.max_uses ? ` / ${code.max_uses}` : ''}</span>
                </TD>
                <TD className="text-xs text-slate-500">{formatDate(code.created_at)}</TD>
                <TD className="text-right">
                  <Button
                    variant="ghost"
                    size="sm"
                    disabled={code.uses_count > 0 || remove.isPending}
                    title={code.uses_count > 0 ? 'used codes are kept for audit' : 'delete'}
                    onClick={() => remove.mutate(code.id)}
                  >
                    <Trash2 className="size-3.5" />
                  </Button>
                </TD>
              </TR>
            ))}
          </TBody>
        </Table>
      </div>

      {error && <ErrorNote message={error} />}
      <DialogFooter>
        <Button type="button" variant="outline" onClick={onClose}>
          Close
        </Button>
      </DialogFooter>
    </div>
  )
}
