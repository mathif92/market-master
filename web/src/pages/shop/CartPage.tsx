import { Link, useNavigate } from 'react-router-dom'
import { useCart } from '@/lib/cart'
import { formatMoney } from '@/lib/format'
import { useAuth } from '@/lib/auth'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { EmptyState } from '@/components/ui/feedback'
import { Trash2, ArrowRight } from 'lucide-react'

export function CartPage() {
  const cart = useCart()
  const { user } = useAuth()
  const navigate = useNavigate()

  if (cart.items.length === 0) {
    return (
      <EmptyState
        title="Your cart is empty"
        description="Browse the catalog and add something you like."
        action={
          <Button asChild>
            <Link to="/">Browse products</Link>
          </Button>
        }
      />
    )
  }

  return (
    <div className="grid gap-6 lg:grid-cols-[1fr_320px]">
      <div className="space-y-4">
        <h1 className="text-2xl font-semibold tracking-tight">Cart</h1>
        {cart.items.map((item) => (
          <div
            key={item.productId}
            className="flex items-center gap-4 rounded-xl border border-slate-200 bg-white p-4 shadow-card"
          >
            <div className="flex size-14 shrink-0 items-center justify-center rounded-lg bg-brand-50 text-xl font-bold text-brand-400">
              {item.name.charAt(0).toUpperCase()}
            </div>
            <div className="min-w-0 flex-1">
              <p className="truncate font-medium text-slate-900">{item.name}</p>
              <p className="text-sm text-slate-500">
                {formatMoney(item.unitPriceCents, item.currency)} each
              </p>
            </div>
            <div className="flex items-center rounded-lg border border-slate-300">
              <button
                className="h-8 w-8 text-slate-500 hover:text-slate-800"
                onClick={() => cart.setQuantity(item.productId, item.quantity - 1)}
              >
                −
              </button>
              <span className="w-7 text-center text-sm font-medium">{item.quantity}</span>
              <button
                className="h-8 w-8 text-slate-500 hover:text-slate-800"
                onClick={() => cart.setQuantity(item.productId, item.quantity + 1)}
              >
                +
              </button>
            </div>
            <p className="w-20 text-right font-medium">
              {formatMoney(item.unitPriceCents * item.quantity, item.currency)}
            </p>
            <button
              className="text-slate-400 hover:text-red-600"
              onClick={() => cart.remove(item.productId)}
              aria-label={`Remove ${item.name}`}
            >
              <Trash2 className="size-4" />
            </button>
          </div>
        ))}
      </div>

      <Card className="h-fit">
        <CardHeader>
          <CardTitle>Summary</CardTitle>
        </CardHeader>
        <CardContent className="space-y-3">
          <div className="flex justify-between text-sm text-slate-600">
            <span>Items</span>
            <span>{cart.count}</span>
          </div>
          <div className="flex justify-between border-t border-slate-200 pt-3 font-semibold">
            <span>Total</span>
            <span>{formatMoney(cart.totalCents)}</span>
          </div>
          <Button
            className="w-full"
            size="lg"
            onClick={() => navigate(user ? '/checkout' : '/login?next=/checkout')}
          >
            {user ? 'Checkout' : 'Sign in to checkout'} <ArrowRight className="size-4" />
          </Button>
          <p className="text-center text-xs text-slate-400">
            Payments are simulated in this demo (no real charges).
          </p>
        </CardContent>
      </Card>
    </div>
  )
}
