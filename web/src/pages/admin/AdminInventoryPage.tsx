import { useState, type FormEvent } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { request, uploadFile, newIdempotencyKey, ApiError } from '@/lib/api'
import type { IngestResult, StockLevel, StockMovement } from '@/lib/types'
import { formatDate } from '@/lib/format'
import { Button } from '@/components/ui/button'
import { Input, Field } from '@/components/ui/input'
import { Card, CardContent, CardHeader, CardTitle, CardDescription } from '@/components/ui/card'
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Table, TBody, TD, TH, THead, TR } from '@/components/ui/table'
import { Badge } from '@/components/ui/badge'
import { PageLoading, ErrorNote, Spinner } from '@/components/ui/feedback'
import { AlertTriangle, ArrowDownUp, Download, SlidersHorizontal, Upload } from 'lucide-react'

export function AdminInventoryPage() {
  const levels = useQuery({
    queryKey: ['stock-levels'],
    queryFn: () => request<{ levels: StockLevel[] }>('/v1/inventory/levels'),
  })
  const moves = useQuery({
    queryKey: ['stock-movements'],
    queryFn: () => request<{ movements: StockMovement[] }>('/v1/inventory/movements?limit=25'),
  })
  const [adjust, setAdjust] = useState<StockLevel | null>(null)

  if (levels.isLoading) return <PageLoading />
  if (levels.error) return <ErrorNote message="Could not load inventory." />

  const list = levels.data?.levels ?? []
  const untracked = list.filter((l) => !l.tracked)
  const lowStock = list.filter(
    (l) => l.tracked && l.available > 0 && l.available <= l.low_stock_threshold,
  )

  return (
    <div className="space-y-6">
      <div className="flex items-start justify-between gap-4">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">Inventory</h1>
          <p className="text-sm text-slate-500">
            Stock levels, manual adjustments and file ingestion. Products start untracked
            (unlimited) until their first batch.
          </p>
        </div>
        <div className="flex gap-2">
          <Button variant="outline" onClick={() => downloadTemplate(list)}>
            <Download className="size-4" /> Template
          </Button>
          <UploadDialog />
        </div>
      </div>

      {untracked.length > 0 && (
        <div className="flex items-start gap-3 rounded-xl border border-amber-200 bg-amber-50 p-4">
          <AlertTriangle className="mt-0.5 size-5 shrink-0 text-amber-600" />
          <div className="text-sm text-amber-800">
            <p className="font-medium">
              {untracked.length} product{untracked.length === 1 ? ' is' : 's are'} not stock-tracked
            </p>
            <p>
              Orders accept any quantity for these products. Use <em>Start tracking</em> or upload
              a first batch to begin enforcing stock.
            </p>
          </div>
        </div>
      )}

      {lowStock.length > 0 && (
        <div className="flex items-start gap-3 rounded-xl border border-sky-200 bg-sky-50 p-4">
          <ArrowDownUp className="mt-0.5 size-5 shrink-0 text-sky-600" />
          <div className="text-sm text-sky-800">
            <p className="font-medium">{lowStock.length} product low on stock</p>
            <p>{lowStock.map((l) => `${l.product_name} (${l.available} left)`).join(' · ')}</p>
          </div>
        </div>
      )}

      <Card>
        <CardHeader>
          <CardTitle>Stock levels</CardTitle>
        </CardHeader>
        <CardContent className="p-0">
          <Table>
            <THead>
              <tr>
                <TH>Product</TH>
                <TH>Status</TH>
                <TH className="text-right">On hand</TH>
                <TH className="text-right">Reserved</TH>
                <TH className="text-right">Available</TH>
                <TH className="text-right">Alert at</TH>
                <TH>Updated</TH>
                <TH className="w-32 text-right">Actions</TH>
              </tr>
            </THead>
            <TBody>
              {list.length === 0 && (
                <TR>
                  <TD colSpan={8} className="py-8 text-center text-slate-500">
                    No products yet — create products in the Catalog tab first.
                  </TD>
                </TR>
              )}
              {list.map((l) => (
                <TR key={l.product_id}>
                  <TD>
                    <p className="font-medium">{l.product_name}</p>
                    <p className="text-xs text-slate-400">{l.product_slug}</p>
                  </TD>
                  <TD>
                    {l.tracked ? (
                      <Badge variant="success">tracked</Badge>
                    ) : (
                      <Badge variant="warning">untracked</Badge>
                    )}
                  </TD>
                  <TD className="text-right font-medium">{l.tracked ? l.on_hand : '—'}</TD>
                  <TD className="text-right text-slate-500">{l.tracked ? l.reserved : '—'}</TD>
                  <TD className="text-right">
                    {!l.tracked ? (
                      <span className="text-slate-400">∞</span>
                    ) : l.available <= 0 ? (
                      <Badge variant="danger">0</Badge>
                    ) : l.available <= l.low_stock_threshold ? (
                      <Badge variant="warning">{l.available}</Badge>
                    ) : (
                      <span className="font-medium">{l.available}</span>
                    )}
                  </TD>
                  <TD className="text-right text-slate-500">
                    {l.tracked ? l.low_stock_threshold : '—'}
                  </TD>
                  <TD className="text-xs text-slate-500">
                    {l.updated_at ? formatDate(l.updated_at) : '—'}
                  </TD>
                  <TD className="text-right">
                    <Button variant="outline" size="sm" onClick={() => setAdjust(l)}>
                      <SlidersHorizontal className="size-3.5" />
                      {l.tracked ? 'Adjust' : 'Start tracking'}
                    </Button>
                  </TD>
                </TR>
              ))}
            </TBody>
          </Table>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>Recent movements</CardTitle>
          <CardDescription>
            Append-only ledger: ingestion, sales and restocks (newest first).
          </CardDescription>
        </CardHeader>
        <CardContent className="p-0">
          <Table>
            <THead>
              <tr>
                <TH>When</TH>
                <TH>Product</TH>
                <TH>Kind</TH>
                <TH>Source</TH>
                <TH className="text-right">Change</TH>
                <TH className="text-right">After</TH>
              </tr>
            </THead>
            <TBody>
              {(moves.data?.movements ?? []).length === 0 && (
                <TR>
                  <TD colSpan={6} className="py-8 text-center text-slate-500">
                    No movements yet.
                  </TD>
                </TR>
              )}
              {(moves.data?.movements ?? []).map((m) => (
                <TR key={m.id}>
                  <TD className="text-xs text-slate-500">{formatDate(m.created_at)}</TD>
                  <TD className="font-medium">{m.product_name}</TD>
                  <TD>
                    <Badge variant={kindVariant(m.kind)}>{m.kind}</Badge>
                  </TD>
                  <TD className="text-slate-500">{m.source}</TD>
                  <TD
                    className={`text-right font-medium ${m.qty_delta < 0 ? 'text-red-600' : 'text-emerald-600'}`}
                  >
                    {m.qty_delta > 0 ? `+${m.qty_delta}` : m.qty_delta}
                  </TD>
                  <TD className="text-right text-slate-600">{m.on_hand_after}</TD>
                </TR>
              ))}
            </TBody>
          </Table>
        </CardContent>
      </Card>

      <AdjustDialog level={adjust} onClose={() => setAdjust(null)} />
    </div>
  )
}

function kindVariant(kind: string): 'success' | 'info' | 'default' | 'danger' {
  switch (kind) {
    case 'sale':
      return 'info'
    case 'restock':
    case 'in':
      return 'success'
    case 'adjust':
      return 'default'
    default:
      return 'default'
  }
}

/** CSV template seeded with this market's SKUs and current on-hand counts. */
function downloadTemplate(levels: StockLevel[]) {
  const rows = ['sku,quantity', ...levels.map((l) => `${l.product_slug},${l.on_hand}`)]
  const blob = new Blob([rows.join('\n') + '\n'], { type: 'text/csv' })
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = 'stock-template.csv'
  a.click()
  URL.revokeObjectURL(url)
}

function IngestReport({ res }: { res: IngestResult }) {
  return (
    <div className="space-y-2 rounded-lg border border-slate-200 bg-slate-50 p-3 text-sm">
      <p className="font-medium text-slate-700">
        {res.replayed
          ? 'Already applied — this batch was a replay, nothing changed.'
          : `Applied ${res.applied_rows} of ${res.total_rows} rows.`}
        {res.error_rows > 0 && ` ${res.error_rows} row(s) failed.`}
      </p>
      {res.errors.length > 0 && (
        <ul className="list-inside list-disc space-y-0.5 text-xs text-red-600">
          {res.errors.map((e, i) => (
            <li key={i}>
              Row {e.row} ({e.sku}): {e.error}
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}

function UploadDialog() {
  const qc = useQueryClient()
  const [open, setOpen] = useState(false)
  const [mode, setMode] = useState('set')
  const [file, setFile] = useState<File | null>(null)
  const [result, setResult] = useState<IngestResult | null>(null)
  const [error, setError] = useState('')

  const upload = useMutation({
    mutationFn: () => {
      const form = new FormData()
      form.append('mode', mode)
      if (file) form.append('file', file)
      return uploadFile<IngestResult>('/v1/inventory/upload', form)
    },
    onSuccess: (res) => {
      setResult(res)
      setError('')
      qc.invalidateQueries({ queryKey: ['stock-levels'] })
      qc.invalidateQueries({ queryKey: ['stock-movements'] })
    },
    onError: (err) => setError(err instanceof ApiError ? err.message : 'Upload failed'),
  })

  function onOpen(v: boolean) {
    setOpen(v)
    if (v) {
      setMode('set')
      setFile(null)
      setResult(null)
      setError('')
    }
  }

  function onSubmit(e: FormEvent) {
    e.preventDefault()
    if (!file) {
      setError('Choose a .csv or .xlsx file.')
      return
    }
    upload.mutate()
  }

  return (
    <>
      <Button onClick={() => onOpen(true)}>
        <Upload className="size-4" /> Upload stock file
      </Button>
      <Dialog open={open} onOpenChange={onOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Upload stock file</DialogTitle>
          </DialogHeader>
          <form onSubmit={onSubmit} className="space-y-4">
            <p className="text-sm text-slate-500">
              CSV or Excel with columns <code>sku</code> (or <code>product_id</code>){' '}
              and <code>quantity</code>. Uploading the same file twice never applies twice.
            </p>
            <Field label="Mode" htmlFor="up-mode">
              <select
                id="up-mode"
                className="flex h-9 w-full rounded-lg border border-slate-300 bg-white px-3 text-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-brand-500"
                value={mode}
                onChange={(e) => setMode(e.target.value)}
              >
                <option value="set">set — replace on_hand with the quantity</option>
                <option value="delta">delta — add the quantity to on_hand</option>
              </select>
            </Field>
            <Field label="File" htmlFor="up-file">
              <input
                id="up-file"
                type="file"
                accept=".csv,.xlsx"
                onChange={(e) => setFile(e.target.files?.[0] ?? null)}
                className="block w-full text-sm text-slate-600 file:mr-3 file:rounded-lg file:border-0 file:bg-brand-50 file:px-3 file:py-1.5 file:text-sm file:font-medium file:text-brand-700 hover:file:bg-brand-100"
              />
            </Field>
            {result && <IngestReport res={result} />}
            {error && <ErrorNote message={error} />}
            <DialogFooter>
              <Button type="button" variant="outline" onClick={() => onOpen(false)}>
                {result ? 'Close' : 'Cancel'}
              </Button>
              {!result && (
                <Button type="submit" disabled={upload.isPending}>
                  {upload.isPending && <Spinner />} Apply
                </Button>
              )}
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>
    </>
  )
}

function AdjustDialog({ level, onClose }: { level: StockLevel | null; onClose: () => void }) {
  return (
    <Dialog open={!!level} onOpenChange={(v) => !v && onClose()}>
      <DialogContent>
        {level && (
          <AdjustForm
            key={`${level.product_id}:${level.updated_at ?? ''}`}
            level={level}
            onClose={onClose}
          />
        )}
      </DialogContent>
    </Dialog>
  )
}

function AdjustForm({ level, onClose }: { level: StockLevel; onClose: () => void }) {
  const qc = useQueryClient()
  const [mode, setMode] = useState('set')
  const [qty, setQty] = useState(String(level.tracked ? level.on_hand : 0))
  const [threshold, setThreshold] = useState(String(level.low_stock_threshold))
  const [result, setResult] = useState<IngestResult | null>(null)
  const [error, setError] = useState('')
  const [key, setKey] = useState(() => newIdempotencyKey())

  const save = useMutation({
    mutationFn: async () => {
      if (Number(threshold) !== level.low_stock_threshold) {
        await request<StockLevel>(`/v1/inventory/levels/${level.product_id}`, {
          method: 'PATCH',
          body: { low_stock_threshold: Number(threshold) },
        })
      }
      return request<IngestResult>('/v1/inventory/ingest', {
        method: 'POST',
        idempotencyKey: key,
        body: {
          mode,
          items: [{ sku: level.product_id, quantity: Number(qty) }],
        },
      })
    },
    onSuccess: (res) => {
      setResult(res)
      setError('')
      qc.invalidateQueries({ queryKey: ['stock-levels'] })
      qc.invalidateQueries({ queryKey: ['stock-movements'] })
    },
    onError: (err) => {
      // a recorded 4xx would replay forever with the same key
      setKey(newIdempotencyKey())
      setError(err instanceof ApiError ? err.message : 'Adjustment failed')
    },
  })

  function onSubmit(e: FormEvent) {
    e.preventDefault()
    const n = Number(qty)
    if (!Number.isInteger(n) || (mode === 'set' && n < 0)) {
      setError(mode === 'set' ? 'Quantity must be a whole number ≥ 0.' : 'Quantity must be a whole number.')
      return
    }
    if (!Number.isInteger(Number(threshold)) || Number(threshold) < 0) {
      setError('Threshold must be a whole number ≥ 0.')
      return
    }
    save.mutate()
  }

  return (
    <form onSubmit={onSubmit} className="space-y-4">
      <div>
        <p className="font-medium">
          {level.tracked ? `Adjust stock — ${level.product_name}` : `Start tracking — ${level.product_name}`}
        </p>
        {!level.tracked && (
          <p className="text-sm text-slate-500">
            This product is untracked (unlimited). Applying an adjustment starts tracking it.
          </p>
        )}
      </div>
      <Field label="Mode" htmlFor="adj-mode">
        <select
          id="adj-mode"
          className="flex h-9 w-full rounded-lg border border-slate-300 bg-white px-3 text-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-brand-500"
          value={mode}
          onChange={(e) => setMode(e.target.value)}
        >
          <option value="set">set — replace on_hand</option>
          <option value="delta">delta — add (negative allowed)</option>
        </select>
      </Field>
      <div className="grid grid-cols-2 gap-4">
        <Field label="Quantity" htmlFor="adj-qty">
          <Input
            id="adj-qty"
            required
            inputMode="numeric"
            value={qty}
            onChange={(e) => setQty(e.target.value)}
            placeholder={mode === 'delta' ? '-5' : '50'}
          />
        </Field>
        <Field label="Alert at (low stock)" htmlFor="adj-th">
          <Input
            id="adj-th"
            required
            inputMode="numeric"
            value={threshold}
            onChange={(e) => setThreshold(e.target.value)}
          />
        </Field>
      </div>
      {result && <IngestReport res={result} />}
      {error && <ErrorNote message={error} />}
      <DialogFooter>
        <Button type="button" variant="outline" onClick={onClose}>
          {result ? 'Close' : 'Cancel'}
        </Button>
        {!result && (
          <Button type="submit" disabled={save.isPending}>
            {save.isPending && <Spinner />} Apply
          </Button>
        )}
      </DialogFooter>
    </form>
  )
}
