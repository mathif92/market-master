import { Link } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { request } from '@/lib/api'
import type { Order, Product, Shipment } from '@/lib/types'
import { formatMoney, formatDate, shortId } from '@/lib/format'
import { StatusBadge } from '@/components/ui/badge'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { PageLoading, ErrorNote, EmptyState } from '@/components/ui/feedback'
import { Package, Boxes, Truck, CircleDollarSign, ArrowRight } from 'lucide-react'

export function DashboardPage() {
  const orders = useQuery({
    queryKey: ['orders', 'all'],
    queryFn: () => request<{ orders: Order[] }>('/v1/orders/all'),
  })
  const products = useQuery({
    queryKey: ['products'],
    queryFn: () => request<{ products: Product[] }>('/v1/products'),
  })
  const shipments = useQuery({
    queryKey: ['shipments'],
    queryFn: () => request<{ shipments: Shipment[] }>('/v1/shipments'),
  })

  if (orders.isLoading || products.isLoading || shipments.isLoading) return <PageLoading />
  if (orders.error || products.error || shipments.error)
    return <ErrorNote message="Could not load the dashboard." />

  const all = orders.data?.orders ?? []
  const paid = all.filter((o) => ['paid', 'dispatched', 'delivered'].includes(o.status))
  const revenue = paid.reduce((n, o) => n + o.total_cents, 0)
  const activeShipments = (shipments.data?.shipments ?? []).filter(
    (s) => s.status === 'dispatched' || s.status === 'in_transit',
  )

  const stats = [
    { label: 'Orders', value: String(all.length), icon: Package, tone: 'text-brand-600 bg-brand-50' },
    { label: 'Products', value: String(products.data?.products.length ?? 0), icon: Boxes, tone: 'text-emerald-600 bg-emerald-50' },
    { label: 'In transit', value: String(activeShipments.length), icon: Truck, tone: 'text-sky-600 bg-sky-50' },
    { label: 'Revenue', value: formatMoney(revenue), icon: CircleDollarSign, tone: 'text-amber-600 bg-amber-50' },
  ]

  const recent = all.slice(0, 8)

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-2xl font-semibold tracking-tight">Dashboard</h1>
        <p className="text-sm text-slate-500">Market overview at a glance.</p>
      </div>

      <div className="grid grid-cols-2 gap-4 lg:grid-cols-4">
        {stats.map((s) => (
          <Card key={s.label}>
            <CardContent className="flex items-center gap-3 p-4">
              <span className={`flex size-10 items-center justify-center rounded-lg ${s.tone}`}>
                <s.icon className="size-5" />
              </span>
              <div>
                <p className="text-xs text-slate-500">{s.label}</p>
                <p className="text-xl font-semibold">{s.value}</p>
              </div>
            </CardContent>
          </Card>
        ))}
      </div>

      <Card>
        <CardHeader className="flex-row items-center justify-between">
          <CardTitle>Recent orders</CardTitle>
          <Link
            to="/admin/orders"
            className="inline-flex items-center gap-1 text-sm font-medium text-brand-600 hover:underline"
          >
            View all <ArrowRight className="size-3.5" />
          </Link>
        </CardHeader>
        <CardContent>
          {recent.length === 0 ? (
            <EmptyState title="No orders yet" description="Orders placed by shoppers appear here." />
          ) : (
            <div className="space-y-2">
              {recent.map((o) => (
                <Link
                  key={o.id}
                  to={`/admin/orders/${o.id}`}
                  className="flex items-center justify-between gap-3 rounded-lg border border-slate-100 px-4 py-3 hover:bg-slate-50"
                >
                  <div>
                    <span className="font-medium">#{shortId(o.id)}</span>
                    <span className="ml-3 text-xs text-slate-500">{formatDate(o.created_at)}</span>
                  </div>
                  <div className="flex items-center gap-3">
                    <StatusBadge status={o.status} />
                    <span className="font-medium">{formatMoney(o.total_cents, o.currency)}</span>
                  </div>
                </Link>
              ))}
            </div>
          )}
        </CardContent>
      </Card>
    </div>
  )
}
