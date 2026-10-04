import { useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { request } from '@/lib/api'
import type { ProductWithStock } from '@/lib/types'
import { effectivePrice, formatDate, formatMoney, formatRule } from '@/lib/format'
import { useCart } from '@/lib/cart'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { PageLoading, ErrorNote } from '@/components/ui/feedback'
import { ArrowLeft, ShoppingCart, Check } from 'lucide-react'

export function ProductPage() {
  const { id } = useParams<{ id: string }>()
  const navigate = useNavigate()
  const cart = useCart()
  const [qty, setQty] = useState(1)

  const { data, isLoading, error } = useQuery({
    queryKey: ['product', id],
    queryFn: () => request<ProductWithStock>(`/v1/products/${id}`),
    enabled: !!id,
  })

  if (isLoading) return <PageLoading />
  if (error || !data) return <ErrorNote message="Product not found." />

  // stock=null → untracked (unlimited). Tracked products gate the cart.
  const stock = data.stock
  const available = stock ? stock.available : null
  const soldOut = available !== null && available <= 0
  const low = available !== null && available > 0 && available <= stock!.low_stock_threshold
  const maxQty = available !== null && available > 0 ? available : 100
  const clampedQty = Math.min(qty, maxQty)

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
          <div className="flex flex-wrap items-baseline gap-3">
            <p
              className={`text-4xl font-semibold ${data.sale_price_cents != null ? 'text-red-600' : 'text-slate-900'}`}
            >
              {formatMoney(effectivePrice(data), data.currency)}
            </p>
            {data.sale_price_cents != null && (
              <p className="text-xl text-slate-400 line-through">
                {formatMoney(data.price_cents, data.currency)}
              </p>
            )}
            {data.campaign && (
              <Badge variant="danger">
                {data.campaign.name} · {formatRule(data.campaign.rule_type, data.campaign.rule_value, data.currency)}
              </Badge>
            )}
            {stock &&
              (soldOut ? (
                <Badge variant="danger">Out of stock</Badge>
              ) : low ? (
                <Badge variant="warning">Only {available} left</Badge>
              ) : (
                <Badge variant="success">In stock · {available}</Badge>
              ))}
          </div>
          {data.campaign && (
            <p className="text-xs text-slate-500">
              Sale ends {formatDate(data.campaign.ends_at)} — the cart is re-priced at checkout.
            </p>
          )}
          <p className="whitespace-pre-line text-sm leading-relaxed text-slate-600">
            {data.description || 'No description provided.'}
          </p>

          <div className="flex items-center gap-3">
            <div className="flex items-center rounded-lg border border-slate-300">
              <button
                className="h-9 w-9 text-slate-500 hover:text-slate-800"
                onClick={() => setQty(Math.max(1, clampedQty - 1))}
                disabled={soldOut}
              >
                −
              </button>
              <span className="w-8 text-center text-sm font-medium">{clampedQty}</span>
              <button
                className="h-9 w-9 text-slate-500 hover:text-slate-800"
                onClick={() => setQty(Math.min(maxQty, clampedQty + 1))}
                disabled={soldOut}
              >
                +
              </button>
            </div>
            <Button
              disabled={soldOut}
              onClick={() =>
                cart.add(
                  {
                    productId: data.id,
                    name: data.name,
                    unitPriceCents: effectivePrice(data),
                    currency: data.currency,
                  },
                  clampedQty,
                )
              }
            >
              {soldOut ? (
                'Out of stock'
              ) : inCart ? (
                <Check className="size-4" />
              ) : (
                <ShoppingCart className="size-4" />
              )}
              {soldOut ? '' : 'Add to cart'}
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
