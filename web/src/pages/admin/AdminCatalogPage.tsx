import { useState, type FormEvent } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { request, ApiError } from '@/lib/api'
import type { Category, Product } from '@/lib/types'
import { formatMoney } from '@/lib/format'
import { Button } from '@/components/ui/button'
import { Input, Field, Textarea } from '@/components/ui/input'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Table, TBody, TD, TH, THead, TR } from '@/components/ui/table'
import { Badge, StatusBadge } from '@/components/ui/badge'
import { PageLoading, ErrorNote, Spinner } from '@/components/ui/feedback'
import { Plus, Pencil, Trash2 } from 'lucide-react'

export function AdminCatalogPage() {
  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-2xl font-semibold tracking-tight">Catalog</h1>
        <p className="text-sm text-slate-500">Categories and products for this market.</p>
      </div>
      <Tabs defaultValue="products">
        <TabsList>
          <TabsTrigger value="products">Products</TabsTrigger>
          <TabsTrigger value="categories">Categories</TabsTrigger>
        </TabsList>
        <TabsContent value="products">
          <ProductsTab />
        </TabsContent>
        <TabsContent value="categories">
          <CategoriesTab />
        </TabsContent>
      </Tabs>
    </div>
  )
}

function useCategories() {
  return useQuery({
    queryKey: ['categories'],
    queryFn: () => request<{ categories: Category[] }>('/v1/categories'),
  })
}

function ProductsTab() {
  const qc = useQueryClient()
  const cats = useCategories()
  const products = useQuery({
    queryKey: ['products'],
    queryFn: () => request<{ products: Product[] }>('/v1/products'),
  })
  const [open, setOpen] = useState(false)
  const [editing, setEditing] = useState<Product | null>(null)
  const [form, setForm] = useState({ name: '', price: '', category_id: '', description: '', status: 'active' })
  const [error, setError] = useState('')

  const catName = (id?: string) => cats.data?.categories.find((c) => c.id === id)?.name

  const save = useMutation({
    mutationFn: () => {
      const body = {
        name: form.name,
        price_cents: Math.round(parseFloat(form.price || '0') * 100),
        category_id: form.category_id || undefined,
        description: form.description,
        status: form.status,
      }
      return editing
        ? request<Product>(`/v1/products/${editing.id}`, { method: 'PATCH', body })
        : request<Product>('/v1/products', { method: 'POST', body })
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['products'] })
      setOpen(false)
      setError('')
    },
    onError: (err) => setError(err instanceof ApiError ? err.message : 'Save failed'),
  })

  function openNew() {
    setEditing(null)
    setForm({ name: '', price: '', category_id: '', description: '', status: 'active' })
    setOpen(true)
  }
  function openEdit(p: Product) {
    setEditing(p)
    setForm({
      name: p.name,
      price: (p.price_cents / 100).toFixed(2),
      category_id: p.category_id ?? '',
      description: p.description,
      status: p.status,
    })
    setOpen(true)
  }

  function onSubmit(e: FormEvent) {
    e.preventDefault()
    save.mutate()
  }

  if (products.isLoading) return <PageLoading />
  if (products.error) return <ErrorNote message="Could not load products." />

  const list = products.data?.products ?? []

  return (
    <>
      <div className="mb-4 flex justify-end">
        <Button onClick={openNew}>
          <Plus className="size-4" /> New product
        </Button>
      </div>

      <Card>
        <CardContent className="p-0">
          <Table>
            <THead>
              <tr>
                <TH>Product</TH>
                <TH>Category</TH>
                <TH>Price</TH>
                <TH>Status</TH>
                <TH className="w-24 text-right">Actions</TH>
              </tr>
            </THead>
            <TBody>
              {list.length === 0 && (
                <TR>
                  <TD colSpan={5} className="py-8 text-center text-slate-500">
                    No products yet — create your first one.
                  </TD>
                </TR>
              )}
              {list.map((p) => (
                <TR key={p.id}>
                  <TD>
                    <p className="font-medium">{p.name}</p>
                    <p className="text-xs text-slate-400">{p.slug}</p>
                  </TD>
                  <TD>{catName(p.category_id) ?? <span className="text-slate-400">—</span>}</TD>
                  <TD className="font-medium">{formatMoney(p.price_cents, p.currency)}</TD>
                  <TD>
                    <StatusBadge status={p.status} />
                  </TD>
                  <TD className="text-right">
                    <div className="inline-flex gap-1">
                      <Button variant="ghost" size="icon" onClick={() => openEdit(p)} aria-label="Edit">
                        <Pencil className="size-4" />
                      </Button>
                    </div>
                  </TD>
                </TR>
              ))}
            </TBody>
          </Table>
        </CardContent>
      </Card>

      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{editing ? 'Edit product' : 'New product'}</DialogTitle>
          </DialogHeader>
          <form onSubmit={onSubmit} className="space-y-4">
            <Field label="Name" htmlFor="p-name">
              <Input
                id="p-name"
                required
                value={form.name}
                onChange={(e) => setForm({ ...form, name: e.target.value })}
              />
            </Field>
            <div className="grid grid-cols-2 gap-4">
              <Field label="Price (USD)" htmlFor="p-price">
                <Input
                  id="p-price"
                  required
                  inputMode="decimal"
                  placeholder="15.00"
                  value={form.price}
                  onChange={(e) => setForm({ ...form, price: e.target.value })}
                />
              </Field>
              <Field label="Category" htmlFor="p-cat">
                <select
                  id="p-cat"
                  className="flex h-9 w-full rounded-lg border border-slate-300 bg-white px-3 text-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-brand-500"
                  value={form.category_id}
                  onChange={(e) => setForm({ ...form, category_id: e.target.value })}
                >
                  <option value="">No category</option>
                  {cats.data?.categories.map((c) => (
                    <option key={c.id} value={c.id}>
                      {c.name}
                    </option>
                  ))}
                </select>
              </Field>
            </div>
            <Field label="Description" htmlFor="p-desc">
              <Textarea
                id="p-desc"
                rows={3}
                value={form.description}
                onChange={(e) => setForm({ ...form, description: e.target.value })}
              />
            </Field>
            <Field label="Status" htmlFor="p-status">
              <select
                id="p-status"
                className="flex h-9 w-full rounded-lg border border-slate-300 bg-white px-3 text-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-brand-500"
                value={form.status}
                onChange={(e) => setForm({ ...form, status: e.target.value })}
              >
                <option value="active">active</option>
                <option value="archived">archived</option>
              </select>
            </Field>
            {error && <ErrorNote message={error} />}
            <DialogFooter>
              <Button type="button" variant="outline" onClick={() => setOpen(false)}>
                Cancel
              </Button>
              <Button type="submit" disabled={save.isPending}>
                {save.isPending && <Spinner />} Save
              </Button>
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>
    </>
  )
}

function CategoriesTab() {
  const qc = useQueryClient()
  const cats = useCategories()
  const [name, setName] = useState('')
  const [error, setError] = useState('')

  const create = useMutation({
    mutationFn: () => request<Category>('/v1/categories', { method: 'POST', body: { name } }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['categories'] })
      setName('')
      setError('')
    },
    onError: (err) => setError(err instanceof ApiError ? err.message : 'Create failed'),
  })

  const remove = useMutation({
    mutationFn: (id: string) => request<void>(`/v1/categories/${id}`, { method: 'DELETE' }),
    onSettled: () => qc.invalidateQueries({ queryKey: ['categories'] }),
    onError: (err) => setError(err instanceof ApiError ? err.message : 'Delete failed'),
  })

  if (cats.isLoading) return <PageLoading />

  return (
    <Card>
      <CardHeader className="flex-row items-center justify-between">
        <CardTitle>Categories</CardTitle>
        <Badge variant="brand">{cats.data?.categories.length ?? 0}</Badge>
      </CardHeader>
      <CardContent className="space-y-4">
        <form
          className="flex gap-2"
          onSubmit={(e) => {
            e.preventDefault()
            if (name.trim()) create.mutate()
          }}
        >
          <Input
            placeholder="New category name"
            value={name}
            onChange={(e) => setName(e.target.value)}
          />
          <Button type="submit" disabled={create.isPending || !name.trim()}>
            {create.isPending ? <Spinner /> : <Plus className="size-4" />} Add
          </Button>
        </form>
        {error && <ErrorNote message={error} />}
        <div className="space-y-2">
          {(cats.data?.categories ?? []).map((c) => (
            <div
              key={c.id}
              className="flex items-center justify-between rounded-lg border border-slate-100 px-4 py-2.5"
            >
              <div>
                <span className="font-medium">{c.name}</span>
                <span className="ml-2 text-xs text-slate-400">{c.slug}</span>
              </div>
              <Button
                variant="ghost"
                size="icon"
                aria-label={`Delete ${c.name}`}
                onClick={() => remove.mutate(c.id)}
                disabled={remove.isPending}
              >
                <Trash2 className="size-4 text-slate-400 hover:text-red-600" />
              </Button>
            </div>
          ))}
          {(cats.data?.categories.length ?? 0) === 0 && (
            <p className="py-4 text-center text-sm text-slate-500">No categories yet.</p>
          )}
        </div>
      </CardContent>
    </Card>
  )
}
