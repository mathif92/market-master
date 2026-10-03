import { Link } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { request } from '@/lib/api'
import type { Order } from '@/lib/types'
import { formatDate, formatMoney, shortId } from '@/lib/format'
import { StatusBadge } from '@/components/ui/badge'
import { EmptyState, PageLoading, ErrorNote } from '@/components/ui/feedback'
import { Button } from '@/components/ui/button'

export function MyOrdersPage() {
  const { data, isLoading, error } = useQuery({
    queryKey: ['orders', 'mine'],
    queryFn: () => request<{ orders: Order[] }>('/v1/orders'),
  })

  if (isLoading) return <PageLoading />
  if (error) return <ErrorNote message="Could not load your orders." />

  const orders = data?.orders ?? []
  if (orders.length === 0) {
    return (
      <EmptyState
        title="No orders yet"
        description="When you place an order it will show up here."
        action={
          <Button asChild>
            <Link to="/">Start shopping</Link>
          </Button>
        }
      />
    )
  }

  return (
    <div className="space-y-4">
      <h1 className="text-2xl font-semibold tracking-tight">My orders</h1>
      <div className="space-y-3">
        {orders.map((o) => (
          <Link
            key={o.id}
            to={`/orders/${o.id}`}
            className="flex items-center justify-between gap-4 rounded-xl border border-slate-200 bg-white p-4 shadow-card transition-shadow hover:shadow-md"
          >
            <div>
              <p className="font-medium text-slate-900">Order {shortId(o.id)}</p>
              <p className="text-sm text-slate-500">{formatDate(o.created_at)}</p>
            </div>
            <div className="flex items-center gap-4">
              <StatusBadge status={o.status} />
              <span className="font-semibold">{formatMoney(o.total_cents, o.currency)}</span>
            </div>
          </Link>
        ))}
      </div>
    </div>
  )
}
