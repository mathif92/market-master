import { useMemo, useState } from 'react'
import { Link } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { request } from '@/lib/api'
import type { Category, Product } from '@/lib/types'
import { effectivePrice, formatMoney, formatRule } from '@/lib/format'
import { useCart } from '@/lib/cart'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { PageLoading, EmptyState, ErrorNote } from '@/components/ui/feedback'
import { cn } from '@/lib/cn'
import { Plus, Check } from 'lucide-react'

export function CatalogPage() {
  const cats = useQuery({ queryKey: ['categories'], queryFn: () => request<{ categories: Category[] }>('/v1/categories') })
  const prods = useQuery({ queryKey: ['products'], queryFn: () => request<{ products: Product[] }>('/v1/products?status=active') })
  const [cat, setCat] = useState<string>('')
  const cart = useCart()

  const categories = cats.data?.categories ?? []
  const products = useMemo(() => {
    const all = prods.data?.products ?? []
    return cat ? all.filter((p) => p.category_id === cat) : all
  }, [prods.data, cat])

  if (cats.isLoading || prods.isLoading) return <PageLoading />
  if (cats.error || prods.error) return <ErrorNote message="Could not load the catalog." />

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-2xl font-semibold tracking-tight">Catalog</h1>
        <p className="text-sm text-slate-500">
          {products.length} product{products.length === 1 ? '' : 's'} available
        </p>
      </div>

      {categories.length > 0 && (
        <div className="flex flex-wrap gap-2">
          <CategoryChip active={cat === ''} onClick={() => setCat('')}>
            All
          </CategoryChip>
          {categories.map((c) => (
            <CategoryChip key={c.id} active={cat === c.id} onClick={() => setCat(c.id)}>
              {c.name}
            </CategoryChip>
          ))}
        </div>
      )}

      {products.length === 0 ? (
        <EmptyState
          title="Nothing here yet"
          description="This market has no products, or this category is empty."
        />
      ) : (
        <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3">
          {products.map((p) => (
            <div
              key={p.id}
              className="group flex flex-col rounded-xl border border-slate-200 bg-white p-5 shadow-card transition-shadow hover:shadow-md"
            >
              <Link to={`/products/${p.id}`} className="flex-1">
                <div className="mb-3 flex h-28 items-end justify-end rounded-lg bg-gradient-to-br from-brand-50 to-slate-100 p-3">
                  <span className="text-3xl font-bold text-brand-300">
                    {p.name.charAt(0).toUpperCase()}
                  </span>
                </div>
                <h3 className="font-medium text-slate-900 group-hover:text-brand-700">{p.name}</h3>
                <p className="mt-1 line-clamp-2 text-sm text-slate-500">{p.description || 'No description'}</p>
              </Link>
              <div className="mt-4 flex items-center justify-between gap-2">
                <div>
                  <div className="flex items-baseline gap-2">
                    <span
                      className={`text-lg font-semibold ${p.sale_price_cents != null ? 'text-red-600' : ''}`}
                    >
                      {formatMoney(effectivePrice(p), p.currency)}
                    </span>
                    {p.sale_price_cents != null && (
                      <span className="text-sm text-slate-400 line-through">
                        {formatMoney(p.price_cents, p.currency)}
                      </span>
                    )}
                  </div>
                  {p.campaign && (
                    <Badge variant="danger" className="mt-1">
                      {p.campaign.name} · {formatRule(p.campaign.rule_type, p.campaign.rule_value, p.currency)}
                    </Badge>
                  )}
                </div>
                <Button
                  size="sm"
                  onClick={() =>
                    cart.add({
                      productId: p.id,
                      name: p.name,
                      unitPriceCents: effectivePrice(p),
                      currency: p.currency,
                    })
                  }
                >
                  {cart.items.some((i) => i.productId === p.id) ? (
                    <>
                      <Check className="size-4" /> Add more
                    </>
                  ) : (
                    <>
                      <Plus className="size-4" /> Add
                    </>
                  )}
                </Button>
              </div>
            </div>
          ))}
        </div>
      )}
    </div>
  )
}

function CategoryChip({
  active,
  onClick,
  children,
}: {
  active: boolean
  onClick: () => void
  children: React.ReactNode
}) {
  return (
    <button
      onClick={onClick}
      className={cn(
        'rounded-full px-3.5 py-1.5 text-sm font-medium transition-colors',
        active
          ? 'bg-brand-600 text-white'
          : 'bg-white text-slate-600 ring-1 ring-slate-200 hover:bg-slate-50',
      )}
    >
      {children}
    </button>
  )
}
