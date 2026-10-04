import { useRef, useState, type FormEvent } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { request, newIdempotencyKey, ApiError } from '@/lib/api'
import { useCart } from '@/lib/cart'
import { formatMoney } from '@/lib/format'
import type { Order, Payment, ShippingMethod } from '@/lib/types'
import { Button } from '@/components/ui/button'
import { Input, Field, Textarea } from '@/components/ui/input'
import { Card, CardContent, CardHeader, CardTitle, CardDescription } from '@/components/ui/card'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { ErrorNote, Spinner } from '@/components/ui/feedback'

/** Fake PSP tokens: anything containing "decline" fails (see payment service). */
const TEST_CARDS = [
  { id: 'tok_visa_ok', label: 'Visa •••• 4242 — approves' },
  { id: 'tok_mc_ok', label: 'Mastercard •••• 5454 — approves' },
  { id: 'tok_visa_decline', label: 'Visa •••• 0002 — declines' },
]

interface Shipping {
  recipient_name: string
  line1: string
  city: string
  country: string
  postal_code: string
}

export function CheckoutPage() {
  const cart = useCart()
  const navigate = useNavigate()
  const queryClient = useQueryClient()

  const methods = useQuery({
    queryKey: ['shipping-methods'],
    queryFn: () => request<{ shipping_methods: ShippingMethod[] }>('/v1/shipping-methods'),
  })

  const [shipping, setShipping] = useState<Shipping>({
    recipient_name: '',
    line1: '',
    city: '',
    country: 'US',
    postal_code: '',
  })
  const [method, setMethod] = useState('')
  const [card, setCard] = useState(TEST_CARDS[0].id)
  const [coupon, setCoupon] = useState('')
  const [error, setError] = useState('')
  const [orderId, setOrderId] = useState('')
  const [payState, setPayState] = useState<'idle' | 'paying' | 'failed'>('idle')

  // One Idempotency-Key per checkout attempt — reused across retries so the
  // order/payment can never be created twice.
  const orderKey = useRef(newIdempotencyKey())

  const shippingOptions = methods.data?.shipping_methods ?? []
  const defaultMethod = shippingOptions.find((m) => m.is_default) ?? shippingOptions[0]

  const selectedMethod = method || defaultMethod?.code || 'standard'
  const shippingCost = 0 // demo: free shipping

  const createOrder = useMutation({
    mutationFn: () =>
      request<{ order: Order }>('/v1/orders', {
        method: 'POST',
        idempotencyKey: orderKey.current,
        body: {
          lines: cart.items.map((i) => ({ product_id: i.productId, quantity: i.quantity })),
          shipping_method_code: selectedMethod,
          shipping,
          ...(coupon.trim() ? { coupon_code: coupon.trim() } : {}),
        },
      }),
    onSuccess: (res) => {
      setOrderId(res.order.id)
      pay(res.order)
    },
    onError: (err) => {
      // The order never landed: retire this attempt's key so a retry with
      // a smaller quantity is not a replay of the stored 4xx.
      orderKey.current = newIdempotencyKey()
      setError(err instanceof ApiError ? err.message : 'Could not create the order')
      setPayState('idle')
    },
  })

  async function pay(order: Order) {
    setPayState('paying')
    setError('')
    try {
      const res = await request<{ payment: Payment }>('/v1/payments', {
        method: 'POST',
        idempotencyKey: newIdempotencyKey(),
        body: { order_id: order.id, payment_method_token: card },
      })
      if (res.payment.status === 'failed') {
        setPayState('failed')
        setError(res.payment.failure_reason || 'Your card was declined.')
        return
      }
      cart.clear()
      queryClient.invalidateQueries({ queryKey: ['orders'] })
      navigate(`/orders/${order.id}`, { replace: true })
    } catch (err) {
      setPayState('failed')
      setError(err instanceof ApiError ? err.message : 'Payment failed')
    }
  }

  function onSubmit(e: FormEvent) {
    e.preventDefault()
    setError('')
    if (cart.items.length === 0) return
    // On a failed payment, keep the same order (its key was consumed) and retry payment only.
    if (orderId) {
      const order = { id: orderId } as Order
      void pay(order)
    } else {
      createOrder.mutate()
    }
  }

  if (cart.items.length === 0) {
    return (
      <div className="py-16 text-center">
        <p className="text-slate-600">Your cart is empty.</p>
        <Button asChild className="mt-4">
          <Link to="/">Back to catalog</Link>
        </Button>
      </div>
    )
  }

  const busy = createOrder.isPending || payState === 'paying'

  return (
    <div className="space-y-6">
      <h1 className="text-2xl font-semibold tracking-tight">Checkout</h1>

      <form onSubmit={onSubmit} className="grid gap-6 lg:grid-cols-[1fr_340px]">
        <div className="space-y-6">
          <Card>
            <CardHeader>
              <CardTitle>Shipping address</CardTitle>
            </CardHeader>
            <CardContent className="grid gap-4 sm:grid-cols-2">
              <div className="sm:col-span-2">
                <Field label="Recipient" htmlFor="recipient_name">
                  <Input
                    id="recipient_name"
                    required
                    value={shipping.recipient_name}
                    onChange={(e) => setShipping({ ...shipping, recipient_name: e.target.value })}
                    placeholder="Ada Lovelace"
                  />
                </Field>
              </div>
              <div className="sm:col-span-2">
                <Field label="Street address" htmlFor="line1">
                  <Textarea
                    id="line1"
                    required
                    rows={2}
                    value={shipping.line1}
                    onChange={(e) => setShipping({ ...shipping, line1: e.target.value })}
                    placeholder="1 Main St"
                  />
                </Field>
              </div>
              <Field label="City" htmlFor="city">
                <Input
                  id="city"
                  required
                  value={shipping.city}
                  onChange={(e) => setShipping({ ...shipping, city: e.target.value })}
                />
              </Field>
              <Field label="Postal code" htmlFor="postal_code">
                <Input
                  id="postal_code"
                  required
                  value={shipping.postal_code}
                  onChange={(e) => setShipping({ ...shipping, postal_code: e.target.value })}
                />
              </Field>
              <Field label="Country" htmlFor="country">
                <Input
                  id="country"
                  required
                  value={shipping.country}
                  onChange={(e) => setShipping({ ...shipping, country: e.target.value })}
                />
              </Field>
              <Field label="Delivery method" htmlFor="method">
                <Select value={selectedMethod} onValueChange={setMethod}>
                  <SelectTrigger id="method">
                    <SelectValue placeholder="Choose a method" />
                  </SelectTrigger>
                  <SelectContent>
                    {shippingOptions.map((m) => (
                      <SelectItem key={m.code} value={m.code}>
                        {m.name} ({m.dispatcher.replace('_', ' ')})
                      </SelectItem>
                    ))}
                    {shippingOptions.length === 0 && (
                      <SelectItem value="standard">Standard</SelectItem>
                    )}
                  </SelectContent>
                </Select>
              </Field>
            </CardContent>
          </Card>

          <Card>
            <CardHeader>
              <CardTitle>Payment</CardTitle>
              <CardDescription>
                Simulated PSP — pick a test card. Any token containing “decline” is declined.
              </CardDescription>
            </CardHeader>
            <CardContent className="space-y-3">
              <Field label="Test card" htmlFor="card">
                <Select value={card} onValueChange={setCard}>
                  <SelectTrigger id="card">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {TEST_CARDS.map((c) => (
                      <SelectItem key={c.id} value={c.id}>
                        {c.label}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </Field>
              <div className="rounded-lg bg-slate-50 p-3 text-xs text-slate-500">
                Card details are never entered — the PSP issues a token and the app only ever
                sends that token.
              </div>
            </CardContent>
          </Card>
        </div>

          <Card className="h-fit">
            <CardHeader>
              <CardTitle>Order summary</CardTitle>
            </CardHeader>
            <CardContent className="space-y-3 text-sm">
              <Field label="Coupon code (optional)" htmlFor="coupon">
                <Input
                  id="coupon"
                  value={coupon}
                  onChange={(e) => setCoupon(e.target.value)}
                  placeholder="e.g. BF50"
                  disabled={!!orderId}
                  autoComplete="off"
                />
              </Field>
              {orderId && (
                <p className="text-xs text-slate-400">
                  Applied when the order was placed — live campaign prices were used above.
                </p>
              )}
              {cart.items.map((i) => (
              <div key={i.productId} className="flex justify-between gap-2">
                <span className="truncate text-slate-600">
                  {i.name} × {i.quantity}
                </span>
                <span className="shrink-0 font-medium">
                  {formatMoney(i.unitPriceCents * i.quantity, i.currency)}
                </span>
              </div>
            ))}
            <div className="flex justify-between border-t border-slate-200 pt-3 text-slate-500">
              <span>Shipping</span>
              <span>{shippingCost === 0 ? 'Free' : formatMoney(shippingCost)}</span>
            </div>
            <div className="flex justify-between border-t border-slate-200 pt-3 text-base font-semibold">
              <span>Total</span>
              <span>{formatMoney(cart.totalCents)}</span>
            </div>

            {error && <ErrorNote message={error} />}
            {orderId && payState === 'idle' && (
              <p className="text-xs text-amber-600">
                Order {orderId.slice(0, 8)} was created; retrying payment will not create a second
                order.
              </p>
            )}

            <Button type="submit" className="w-full" size="lg" disabled={busy}>
              {busy && <Spinner />}
              {busy
                ? payState === 'paying'
                  ? 'Processing payment…'
                  : 'Placing order…'
                : 'Pay and place order'}
            </Button>
          </CardContent>
        </Card>
      </form>
    </div>
  )
}
