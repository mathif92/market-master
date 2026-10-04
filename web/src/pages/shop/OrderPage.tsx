import { Link, useParams } from 'react-router-dom'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { request, ApiError } from '@/lib/api'
import type { Order, Payment, Shipment } from '@/lib/types'
import { formatDate, formatMoney, shortId } from '@/lib/format'
import { StatusBadge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { PageLoading, ErrorNote, Spinner } from '@/components/ui/feedback'
import { ArrowLeft, Package, Truck, CreditCard } from 'lucide-react'

const FLOW = ['created', 'paid', 'dispatched', 'delivered'] as const
const CANCELABLE: Order['status'][] = ['created', 'awaiting_payment', 'payment_failed', 'paid']

export function OrderPage() {
  const { id } = useParams<{ id: string }>()
  const queryClient = useQueryClient()

  // Poll while the order is mid-flight (events → status arrive async).
  const { data, isLoading, error } = useQuery({
    queryKey: ['order', id],
    queryFn: () => request<{ order: Order }>(`/v1/orders/${id}`),
    enabled: !!id,
    refetchInterval: (query) => {
      const s = query.state.data?.order.status
      return s && s !== 'delivered' && s !== 'cancelled' ? 2000 : false
    },
  })

  const payments = useQuery({
    queryKey: ['payments', 'order', id],
    queryFn: () => request<{ payments: Payment[] }>(`/v1/payments?order_id=${id}`),
    enabled: !!id,
    refetchInterval: 4000,
  })

  const shipments = useQuery({
    queryKey: ['shipments', 'order', id],
    queryFn: () => request<{ shipments: Shipment[] }>(`/v1/shipments?order_id=${id}`),
    enabled: !!id,
    refetchInterval: 4000,
  })

  const cancel = useMutation({
    mutationFn: () => request<{ order: Order }>(`/v1/orders/${id}/cancel`, { method: 'POST' }),
    onSettled: () => queryClient.invalidateQueries({ queryKey: ['order', id] }),
  })

  if (isLoading) return <PageLoading />
  if (error || !data) return <ErrorNote message="Order not found." />

  const o = data.order
  const payment = payments.data?.payments?.[0]
  const shipment = shipments.data?.shipments?.[0]

  return (
    <div className="mx-auto max-w-3xl space-y-6">
      <div className="flex items-center justify-between">
        <Link to="/orders" className="inline-flex items-center gap-1.5 text-sm text-slate-500 hover:text-slate-700">
          <ArrowLeft className="size-4" /> My orders
        </Link>
        <StatusBadge status={o.status} />
      </div>

      <div>
        <h1 className="text-2xl font-semibold tracking-tight">Order {shortId(o.id)}</h1>
        <p className="text-sm text-slate-500">{formatDate(o.created_at)}</p>
      </div>

      {/* progress */}
      {o.status !== 'cancelled' && o.status !== 'payment_failed' && (
        <div className="flex items-center gap-2">
          {FLOW.map((stage) => {
            const idx = FLOW.indexOf(stage)
            const cur = FLOW.indexOf(o.status as (typeof FLOW)[number])
            const done = cur >= 0 && idx <= cur
            return (
              <div key={stage} className="flex flex-1 items-center gap-2">
                <div
                  className={`flex size-7 shrink-0 items-center justify-center rounded-full text-xs font-semibold ${
                    done ? 'bg-brand-600 text-white' : 'bg-slate-200 text-slate-500'
                  }`}
                >
                  {idx + 1}
                </div>
                <span className={`text-xs capitalize ${done ? 'text-slate-900' : 'text-slate-400'}`}>
                  {stage.replace('_', ' ')}
                </span>
                {idx < FLOW.length - 1 && (
                  <div className={`h-0.5 flex-1 ${done && cur > idx ? 'bg-brand-600' : 'bg-slate-200'}`} />
                )}
              </div>
            )
          })}
        </div>
      )}

      <div className="grid gap-4 md:grid-cols-2">
        <Card>
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-base">
              <Package className="size-4" /> Items
            </CardTitle>
          </CardHeader>
          <CardContent className="space-y-2 text-sm">
            {o.lines?.length ? (
              o.lines.map((l, i) => {
                const lineList = (l.list_price_cents || l.unit_price_cents) * l.quantity
                const linePaid = l.unit_price_cents * l.quantity
                const discounted = lineList > linePaid
                return (
                  <div key={i} className="flex justify-between gap-2">
                    <span className="truncate text-slate-600">
                      {l.name} × {l.quantity}
                    </span>
                    <span className="shrink-0">
                      {discounted && (
                        <span className="mr-1.5 text-slate-400 line-through">
                          {formatMoney(lineList, o.currency)}
                        </span>
                      )}
                      <span className={discounted ? 'font-medium text-red-600' : 'font-medium'}>
                        {formatMoney(linePaid, o.currency)}
                      </span>
                    </span>
                  </div>
                )
              })
            ) : (
              <p className="text-slate-500">—</p>
            )}
            {o.lines?.some((l) => (l.list_price_cents || l.unit_price_cents) > l.unit_price_cents) && (
              <div className="flex justify-between text-emerald-700">
                <span>Campaign savings</span>
                <span className="font-medium">
                  −
                  {formatMoney(
                    o.lines.reduce(
                      (sum, l) =>
                        sum +
                        ((l.list_price_cents || l.unit_price_cents) - l.unit_price_cents) * l.quantity,
                      0,
                    ),
                    o.currency,
                  )}
                </span>
              </div>
            )}
            <div className="flex justify-between border-t border-slate-200 pt-2 font-semibold">
              <span>Total</span>
              <span>{formatMoney(o.total_cents, o.currency)}</span>
            </div>
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-base">
              <CreditCard className="size-4" /> Payment
            </CardTitle>
          </CardHeader>
          <CardContent className="space-y-1.5 text-sm">
            {payment ? (
              <>
                <div className="flex justify-between">
                  <span className="text-slate-500">Status</span>
                  <StatusBadge status={payment.status} />
                </div>
                <div className="flex justify-between">
                  <span className="text-slate-500">Method</span>
                  <span className="font-medium uppercase">
                    {payment.brand} •••• {payment.last4}
                  </span>
                </div>
                <div className="flex justify-between">
                  <span className="text-slate-500">Reference</span>
                  <span className="font-mono text-xs">{payment.psp_ref || '—'}</span>
                </div>
                {payment.failure_reason && (
                  <p className="text-red-600">{payment.failure_reason}</p>
                )}
              </>
            ) : (
              <p className="text-slate-500">No payment yet.</p>
            )}
          </CardContent>
        </Card>

        <Card className="md:col-span-2">
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-base">
              <Truck className="size-4" /> Delivery
            </CardTitle>
          </CardHeader>
          <CardContent className="grid gap-4 text-sm sm:grid-cols-2">
            <div className="space-y-1">
              <p className="font-medium">{o.recipient_name}</p>
              <p className="text-slate-600">{o.address_line}</p>
              <p className="text-slate-600">
                {o.city}, {o.postal_code}, {o.country}
              </p>
              <p className="text-slate-500">Method: {o.shipping_method_code}</p>
            </div>
            <div className="space-y-2">
              {shipment ? (
                <>
                  <div className="flex items-center gap-2">
                    <StatusBadge status={shipment.status} />
                    <span className="text-xs text-slate-500">
                      via {shipment.dispatcher.replace('_', ' ')}
                    </span>
                  </div>
                  <p className="text-slate-600">
                    Tracking: <span className="font-mono text-xs">{shipment.provider_ref}</span>
                  </p>
                </>
              ) : (
                <p className="text-slate-500">
                  {o.status === 'paid'
                    ? 'Dispatching soon…'
                    : 'Shipment created after payment.'}
                </p>
              )}
            </div>
          </CardContent>
        </Card>
      </div>

      {CANCELABLE.includes(o.status) && (
        <div className="flex justify-end">
          <Button
            variant="outline"
            disabled={cancel.isPending}
            onClick={() => cancel.mutate()}
          >
            {cancel.isPending && <Spinner />} Cancel order
          </Button>
        </div>
      )}
      {cancel.error instanceof ApiError && <ErrorNote message={cancel.error.message} />}
    </div>
  )
}
