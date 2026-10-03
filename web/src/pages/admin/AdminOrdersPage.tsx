import { useState } from 'react'
import { Link } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { request } from '@/lib/api'
import type { Order, OrderStatus } from '@/lib/types'
import { formatDate, formatMoney, shortId } from '@/lib/format'
import { StatusBadge } from '@/components/ui/badge'
import { Card, CardContent } from '@/components/ui/card'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Table, TBody, TD, TH, THead, TR } from '@/components/ui/table'
import { PageLoading, ErrorNote, EmptyState } from '@/components/ui/feedback'

const STATUSES: (OrderStatus | 'all')[] = [
  'all',
  'created',
  'awaiting_payment',
  'payment_failed',
  'paid',
  'dispatched',
  'delivered',
  'cancelled',
]

export function AdminOrdersPage() {
  const [status, setStatus] = useState('all')
  const { data, isLoading, error } = useQuery({
    queryKey: ['orders', 'all'],
    queryFn: () => request<{ orders: Order[] }>('/v1/orders/all'),
  })

  if (isLoading) return <PageLoading />
  if (error) return <ErrorNote message="Could not load orders." />

  const all = data?.orders ?? []
  const list = status === 'all' ? all : all.filter((o) => o.status === status)

  return (
    <div className="space-y-6">
      <div className="flex items-end justify-between">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">Orders</h1>
          <p className="text-sm text-slate-500">{all.length} orders in this market.</p>
        </div>
        <div className="w-48">
          <Select value={status} onValueChange={setStatus}>
            <SelectTrigger>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {STATUSES.map((s) => (
                <SelectItem key={s} value={s}>
                  {s === 'all' ? 'All statuses' : s.replace(/_/g, ' ')}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
      </div>

      {list.length === 0 ? (
        <EmptyState title="No orders match this filter" />
      ) : (
        <Card>
          <CardContent className="p-0">
            <Table>
              <THead>
                <tr>
                  <TH>Order</TH>
                  <TH>Placed</TH>
                  <TH>Ship to</TH>
                  <TH>Status</TH>
                  <TH className="text-right">Total</TH>
                </tr>
              </THead>
              <TBody>
                {list.map((o) => (
                  <TR key={o.id}>
                    <TD>
                      <Link
                        to={`/admin/orders/${o.id}`}
                        className="font-medium text-brand-600 hover:underline"
                      >
                        #{shortId(o.id)}
                      </Link>
                    </TD>
                    <TD className="text-slate-500">{formatDate(o.created_at)}</TD>
                    <TD>
                      <p className="font-medium">{o.recipient_name}</p>
                      <p className="text-xs text-slate-400">
                        {o.city}, {o.country}
                      </p>
                    </TD>
                    <TD>
                      <StatusBadge status={o.status} />
                    </TD>
                    <TD className="text-right font-medium">
                      {formatMoney(o.total_cents, o.currency)}
                    </TD>
                  </TR>
                ))}
              </TBody>
            </Table>
          </CardContent>
        </Card>
      )}
    </div>
  )
}
