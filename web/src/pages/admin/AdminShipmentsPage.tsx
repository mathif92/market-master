import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link } from 'react-router-dom'
import { request, ApiError } from '@/lib/api'
import type { Shipment } from '@/lib/types'
import { formatDate, shortId } from '@/lib/format'
import { StatusBadge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { Table, TBody, TD, TH, THead, TR } from '@/components/ui/table'
import { PageLoading, ErrorNote, EmptyState, Spinner } from '@/components/ui/feedback'
import { CheckCircle2, XCircle } from 'lucide-react'

export function AdminShipmentsPage() {
  const qc = useQueryClient()
  const { data, isLoading, error } = useQuery({
    queryKey: ['shipments'],
    queryFn: () => request<{ shipments: Shipment[] }>('/v1/shipments'),
    refetchInterval: 5000,
  })

  const deliver = useMutation({
    mutationFn: (id: string) => request(`/v1/shipments/${id}/deliver`, { method: 'POST' }),
    onSettled: () => qc.invalidateQueries({ queryKey: ['shipments'] }),
  })
  const cancel = useMutation({
    mutationFn: (id: string) => request(`/v1/shipments/${id}/cancel`, { method: 'POST' }),
    onSettled: () => qc.invalidateQueries({ queryKey: ['shipments'] }),
  })

  if (isLoading) return <PageLoading />
  if (error) return <ErrorNote message="Could not load shipments." />

  const list = data?.shipments ?? []

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-2xl font-semibold tracking-tight">Shipments</h1>
        <p className="text-sm text-slate-500">
          Dispatch happens automatically after payment; deliver/cancel from here.
        </p>
      </div>

      {deliver.error && (
        <ErrorNote message={deliver.error instanceof ApiError ? deliver.error.message : 'Deliver failed'} />
      )}
      {cancel.error && (
        <ErrorNote message={cancel.error instanceof ApiError ? cancel.error.message : 'Cancel failed'} />
      )}

      {list.length === 0 ? (
        <EmptyState
          title="No shipments yet"
          description="Shipments are created automatically once an order is paid."
        />
      ) : (
        <Card>
          <CardContent className="p-0">
            <Table>
              <THead>
                <tr>
                  <TH>Shipment</TH>
                  <TH>Order</TH>
                  <TH>Recipient</TH>
                  <TH>Dispatcher</TH>
                  <TH>Status</TH>
                  <TH>Updated</TH>
                  <TH className="text-right">Actions</TH>
                </tr>
              </THead>
              <TBody>
                {list.map((s) => (
                  <TR key={s.id}>
                    <TD className="font-mono text-xs">{shortId(s.id)}</TD>
                    <TD>
                      <Link
                        to={`/admin/orders/${s.order_id}`}
                        className="font-medium text-brand-600 hover:underline"
                      >
                        #{shortId(s.order_id)}
                      </Link>
                    </TD>
                    <TD>
                      <p>{s.recipient_name}</p>
                      <p className="text-xs text-slate-400">
                        {s.city}, {s.country}
                      </p>
                    </TD>
                    <TD className="capitalize">{s.dispatcher.replace('_', ' ')}</TD>
                    <TD>
                      <StatusBadge status={s.status} />
                    </TD>
                    <TD className="text-slate-500">{formatDate(s.updated_at)}</TD>
                    <TD className="text-right">
                      <div className="inline-flex gap-1">
                        {(s.status === 'dispatched' || s.status === 'in_transit') && (
                          <>
                            <Button
                              size="sm"
                              variant="outline"
                              disabled={deliver.isPending}
                              onClick={() => deliver.mutate(s.id)}
                            >
                              {deliver.isPending ? <Spinner /> : <CheckCircle2 className="size-4" />}
                              Deliver
                            </Button>
                            <Button
                              size="sm"
                              variant="ghost"
                              disabled={cancel.isPending}
                              onClick={() => cancel.mutate(s.id)}
                            >
                              <XCircle className="size-4" /> Cancel
                            </Button>
                          </>
                        )}
                      </div>
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
