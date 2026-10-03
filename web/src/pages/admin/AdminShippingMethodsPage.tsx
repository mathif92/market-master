import { useState, type FormEvent } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { request, ApiError } from '@/lib/api'
import type { ShippingMethod } from '@/lib/types'
import { Button } from '@/components/ui/button'
import { Input, Field } from '@/components/ui/input'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Badge } from '@/components/ui/badge'
import { PageLoading, ErrorNote, Spinner } from '@/components/ui/feedback'
import { Plus, Trash2 } from 'lucide-react'

export function AdminShippingMethodsPage() {
  const qc = useQueryClient()
  const { data, isLoading, error } = useQuery({
    queryKey: ['shipping-methods'],
    queryFn: () => request<{ shipping_methods: ShippingMethod[] }>('/v1/shipping-methods'),
  })

  const [open, setOpen] = useState(false)
  const [form, setForm] = useState({ code: '', name: '', dispatcher: 'own_fleet', is_default: false })
  const [formError, setFormError] = useState('')

  const create = useMutation({
    mutationFn: () =>
      request<ShippingMethod>('/v1/shipping-methods', {
        method: 'POST',
        body: {
          code: form.code,
          name: form.name,
          dispatcher: form.dispatcher,
          is_default: form.is_default,
        },
      }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['shipping-methods'] })
      setOpen(false)
      setForm({ code: '', name: '', dispatcher: 'own_fleet', is_default: false })
      setFormError('')
    },
    onError: (err) => setFormError(err instanceof ApiError ? err.message : 'Create failed'),
  })

  const remove = useMutation({
    mutationFn: (id: string) => request<void>(`/v1/shipping-methods/${id}`, { method: 'DELETE' }),
    onSettled: () => qc.invalidateQueries({ queryKey: ['shipping-methods'] }),
  })

  if (isLoading) return <PageLoading />
  if (error) return <ErrorNote message="Could not load shipping methods." />

  return (
    <div className="space-y-6">
      <div className="flex items-end justify-between">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">Shipping methods</h1>
          <p className="text-sm text-slate-500">
            Per-market delivery options; checkout offers these to shoppers.
          </p>
        </div>
        <Button onClick={() => setOpen(true)}>
          <Plus className="size-4" /> New method
        </Button>
      </div>

      <div className="grid gap-4 sm:grid-cols-2">
        {(data?.shipping_methods ?? []).map((m) => (
          <Card key={m.id}>
            <CardHeader className="flex-row items-start justify-between">
              <div>
                <CardTitle className="text-base">{m.name}</CardTitle>
                <p className="mt-1 font-mono text-xs text-slate-400">{m.code}</p>
              </div>
              <div className="flex items-center gap-2">
                {m.is_default && <Badge variant="brand">default</Badge>}
                <Button
                  variant="ghost"
                  size="icon"
                  aria-label={`Delete ${m.name}`}
                  disabled={remove.isPending}
                  onClick={() => remove.mutate(m.id)}
                >
                  <Trash2 className="size-4 text-slate-400 hover:text-red-600" />
                </Button>
              </div>
            </CardHeader>
            <CardContent className="text-sm text-slate-600">
              Dispatcher: <span className="capitalize">{m.dispatcher.replace('_', ' ')}</span>
            </CardContent>
          </Card>
        ))}
        {(data?.shipping_methods.length ?? 0) === 0 && (
          <p className="text-sm text-slate-500">No methods configured yet.</p>
        )}
      </div>

      {remove.error && (
        <ErrorNote message={remove.error instanceof ApiError ? remove.error.message : 'Delete failed'} />
      )}

      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>New shipping method</DialogTitle>
          </DialogHeader>
          <form
            className="space-y-4"
            onSubmit={(e: FormEvent) => {
              e.preventDefault()
              create.mutate()
            }}
          >
            <Field label="Code" htmlFor="m-code" hint="Short unique code, e.g. express">
              <Input
                id="m-code"
                required
                value={form.code}
                onChange={(e) => setForm({ ...form, code: e.target.value })}
                placeholder="express"
              />
            </Field>
            <Field label="Display name" htmlFor="m-name">
              <Input
                id="m-name"
                required
                value={form.name}
                onChange={(e) => setForm({ ...form, name: e.target.value })}
                placeholder="Express delivery"
              />
            </Field>
            <Field label="Dispatcher" htmlFor="m-dispatcher">
              <Select
                value={form.dispatcher}
                onValueChange={(v) => setForm({ ...form, dispatcher: v })}
              >
                <SelectTrigger id="m-dispatcher">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="own_fleet">Own fleet</SelectItem>
                  <SelectItem value="third_party">Third party</SelectItem>
                </SelectContent>
              </Select>
            </Field>
            <label className="flex items-center gap-2 text-sm">
              <input
                type="checkbox"
                checked={form.is_default}
                onChange={(e) => setForm({ ...form, is_default: e.target.checked })}
                className="size-4 rounded border-slate-300 accent-brand-600"
              />
              Default method for checkout
            </label>
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
