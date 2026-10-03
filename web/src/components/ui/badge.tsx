import * as React from 'react'
import { cva, type VariantProps } from 'class-variance-authority'
import { cn } from '@/lib/cn'

const badgeVariants = cva(
  'inline-flex items-center rounded-full px-2.5 py-0.5 text-xs font-medium ring-1 ring-inset',
  {
    variants: {
      variant: {
        default: 'bg-slate-100 text-slate-700 ring-slate-200',
        brand: 'bg-brand-50 text-brand-700 ring-brand-200',
        success: 'bg-emerald-50 text-emerald-700 ring-emerald-200',
        warning: 'bg-amber-50 text-amber-700 ring-amber-200',
        danger: 'bg-red-50 text-red-700 ring-red-200',
        info: 'bg-sky-50 text-sky-700 ring-sky-200',
      },
    },
    defaultVariants: { variant: 'default' },
  },
)

export interface BadgeProps
  extends React.HTMLAttributes<HTMLSpanElement>,
    VariantProps<typeof badgeVariants> {}

export function Badge({ className, variant, ...props }: BadgeProps) {
  return <span className={cn(badgeVariants({ variant }), className)} {...props} />
}

/** Badge colors for order/shipment states. */
export type BadgeVariant = VariantProps<typeof badgeVariants>['variant']

export function statusVariant(status: string): BadgeVariant {
  switch (status) {
    case 'paid':
    case 'captured':
    case 'delivered':
    case 'active':
      return 'success'
    case 'dispatched':
    case 'in_transit':
    case 'awaiting_payment':
    case 'authorized':
    case 'pending':
      return 'info'
    case 'payment_failed':
    case 'failed':
    case 'cancelled':
      return 'danger'
    case 'created':
      return 'brand'
    default:
      return 'default'
  }
}

export function StatusBadge({ status }: { status: string }) {
  return <Badge variant={statusVariant(status)}>{status.replace(/_/g, ' ')}</Badge>
}
