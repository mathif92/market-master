import { useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { request } from '@/lib/api'
import type { Product } from '@/lib/types'
import { formatMoney } from '@/lib/format'
import { useCart } from '@/lib/cart'
import { Button } from '@/components/ui/button'
import { PageLoading, ErrorNote } from '@/components/ui/feedback'
import { ArrowLeft, ShoppingCart, Check } from 'lucide-react'

export function ProductPage() {
  const { id } = useParams<{ id: string }>()
  const navigate = useNavigate()
  const cart = useCart()
  const [qty, setQty] = useState(1)

  const { data, isLoading, error } = useQuery({
    queryKey: ['product', id],
    queryFn: () => request<Product>(`/v1/products/${id}`),
    enabled: !!id,
  })

  if (isLoading) return <PageLoading />
  if (error || !data) return <ErrorNote message="Product not found." />

  const inCart = cart.items.some((i) => i.productId === data.id)

  return (
    <div className="space-y-6">
      <Link to="/" className="inline-flex items-center gap-1.5 text-sm text-slate-500 hover:text-slate-700">
        <ArrowLeft className="size-4" /> Back to catalog
      </Link>

      <div className="grid gap-8 md:grid-cols-2">
        <div className="flex h-72 items-end justify-end rounded-xl bg-gradient-to-br from-brand-50 to-slate-100 p-6">
          <span className="text-7xl font-bold text-brand-200">
            {data.name.charAt(0).toUpperCase()}
          </span>
        </div>

        <div className="space-y-4">
          <div>
            <h1 className="text-3xl font-semibold tracking-tight">{data.name}</h1>
            <p className="mt-1 text-sm text-slate-500">
              {data.category_id ? 'Categorized product' : 'Uncategorized'} · SKU {data.slug}
            </p>
          </div>
          <p className="text-4xl font-semibold text-slate-900">
            {formatMoney(data.price_cents, data.currency)}
          </p>
          <p className="whitespace-pre-line text-sm leading-relaxed text-slate-600">
            {data.description || 'No description provided.'}
          </p>

          <div className="flex items-center gap-3">
            <div className="flex items-center rounded-lg border border-slate-300">
              <button
                className="h-9 w-9 text-slate-500 hover:text-slate-800"
                onClick={() => setQty(Math.max(1, qty - 1))}
              >
                −
              </button>
              <span className="w-8 text-center text-sm font-medium">{qty}</span>
              <button
                className="h-9 w-9 text-slate-500 hover:text-slate-800"
                onClick={() => setQty(qty + 1)}
              >
                +
              </button>
            </div>
            <Button
              onClick={() =>
                cart.add(
                  {
                    productId: data.id,
                    name: data.name,
                    unitPriceCents: data.price_cents,
                    currency: data.currency,
                  },
                  qty,
                )
              }
            >
              {inCart ? <Check className="size-4" /> : <ShoppingCart className="size-4" />}
              Add to cart
            </Button>
            <Button variant="outline" onClick={() => navigate('/cart')}>
              Go to cart
            </Button>
          </div>
        </div>
      </div>
    </div>
  )
}
