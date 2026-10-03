import { Link, useParams } from 'react-router-dom'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { request } from '@/lib/api'
import type { Order, Payment, Shipment } from '@/lib/types'
import { formatDate, formatMoney, shortId } from '@/lib/format'
import { StatusBadge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { PageLoading, ErrorNote, Spinner } from '@/components/ui/feedback'
import { ArrowLeft, Package, Truck, CreditCard } from 'lucide-react'

export function AdminOrderDetailPage() {
  const { id } = useParams<{ id: string }>()
  const qc = useQueryClient()

  const order = useQuery({
    queryKey: ['order', id],
    queryFn: () => request<{ order: Order }>(`/v1/orders/${id}`),
    enabled: !!id,
    refetchInterval: (q) => {
      const s = q.state.data?.order.status
      return s && s !== 'delivered' && s !== 'cancelled' ? 3000 : false
    },
  })
  const payments = useQuery({
    queryKey: ['payments', 'order', id],
    queryFn: () => request<{ payments: Payment[] }>(`/v1/payments?order_id=${id}`),
    enabled: !!id,
  })
  const shipments = useQuery({
    queryKey: ['shipments', 'order', id],
    queryFn: () => request<{ shipments: Shipment[] }>(`/v1/shipments?order_id=${id}`),
    enabled: !!id,
    refetchInterval: 3000,
  })

  const cancelOrder = useMutation({
    mutationFn: () => request(`/v1/orders/${id}/cancel`, { method: 'POST' }),
    onSettled: () => qc.invalidateQueries({ queryKey: ['order', id] }),
  })
  const deliver = useMutation({
    mutationFn: (sid: string) => request(`/v1/shipments/${sid}/deliver`, { method: 'POST' }),
    onSettled: () => {
      qc.invalidateQueries({ queryKey: ['shipments', 'order', id] })
      qc.invalidateQueries({ queryKey: ['order', id] })
    },
  })

  if (order.isLoading) return <PageLoading />
  if (order.error || !order.data) return <ErrorNote message="Order not found." />

  const o = order.data.order
  const payment = payments.data?.payments?.[0]
  const shipment = shipments.data?.shipments?.[0]

  return (
    <div className="space-y-6">
      <Link
        to="/admin/orders"
        className="inline-flex items-center gap-1.5 text-sm text-slate-500 hover:text-slate-700"
      >
        <ArrowLeft className="size-4" /> All orders
      </Link>

      <div className="flex items-start justify-between">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">Order {shortId(o.id)}</h1>
          <p className="text-sm text-slate-500">
            {formatDate(o.created_at)} · customer {shortId(o.customer_id)}
          </p>
        </div>
        <StatusBadge status={o.status} />
      </div>

      <div className="grid gap-4 md:grid-cols-3">
        <Card>
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-base">
              <Package className="size-4" /> Items
            </CardTitle>
          </CardHeader>
          <CardContent className="space-y-2 text-sm">
            {o.lines?.map((l, i) => (
              <div key={i} className="flex justify-between gap-2">
                <span className="truncate text-slate-600">
                  {l.name} × {l.quantity}
                </span>
                <span className="shrink-0 font-medium">
                  {formatMoney(l.unit_price_cents * l.quantity, o.currency)}
                </span>
              </div>
            ))}
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
          <CardContent className="space-y-2 text-sm">
            {payment ? (
              <>
                <div className="flex justify-between">
                  <span className="text-slate-500">Status</span>
                  <StatusBadge status={payment.status} />
                </div>
                <div className="flex justify-between">
                  <span className="text-slate-500">Card</span>
                  <span className="uppercase">
                    {payment.brand} •••• {payment.last4}
                  </span>
                </div>
                <div className="flex justify-between">
                  <span className="text-slate-500">PSP ref</span>
                  <span className="font-mono text-xs">{payment.psp_ref || '—'}</span>
                </div>
                {payment.failure_reason && (
                  <p className="text-red-600">{payment.failure_reason}</p>
                )}
              </>
            ) : (
              <p className="text-slate-500">No payment attempt yet.</p>
            )}
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-base">
              <Truck className="size-4" /> Shipment
            </CardTitle>
          </CardHeader>
          <CardContent className="space-y-3 text-sm">
            {shipment ? (
              <>
                <div className="flex justify-between">
                  <span className="text-slate-500">Status</span>
                  <StatusBadge status={shipment.status} />
                </div>
                <p className="text-slate-600">
                  {shipment.recipient_name}, {shipment.address_line}, {shipment.city}
                </p>
                <p className="font-mono text-xs text-slate-500">{shipment.provider_ref}</p>
                {(shipment.status === 'dispatched' || shipment.status === 'in_transit') && (
                  <Button
                    size="sm"
                    className="w-full"
                    disabled={deliver.isPending}
                    onClick={() => deliver.mutate(shipment.id)}
                  >
                    {deliver.isPending && <Spinner />} Mark delivered
                  </Button>
                )}
              </>
            ) : (
              <p className="text-slate-500">
                {o.status === 'paid' ? 'Dispatching soon…' : 'Created after payment.'}
              </p>
            )}
          </CardContent>
        </Card>
      </div>

      <Card>
        <CardHeader>
          <CardTitle className="text-base">Shipping details</CardTitle>
        </CardHeader>
        <CardContent className="text-sm text-slate-600">
          <p className="font-medium text-slate-900">{o.recipient_name}</p>
          <p>{o.address_line}</p>
          <p>
            {o.city}, {o.postal_code}, {o.country}
          </p>
          <p className="mt-1 text-slate-500">Method: {o.shipping_method_code}</p>
        </CardContent>
      </Card>

      {['created', 'awaiting_payment', 'payment_failed', 'paid'].includes(o.status) && (
        <div className="flex justify-end">
          <Button
            variant="destructive"
            disabled={cancelOrder.isPending}
            onClick={() => cancelOrder.mutate()}
          >
            {cancelOrder.isPending && <Spinner />} Cancel order
          </Button>
        </div>
      )}
      {cancelOrder.error instanceof Error && (
        <ErrorNote message={cancelOrder.error.message} />
      )}
    </div>
  )
}
