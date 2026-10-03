export interface Category {
  id: string
  parent_id?: string
  name: string
  slug: string
  created_at: string
  updated_at: string
}

export interface Product {
  id: string
  category_id?: string
  name: string
  slug: string
  description: string
  price_cents: number
  currency: string
  status: 'active' | 'archived'
  created_at: string
  updated_at: string
}

export type OrderStatus =
  | 'created'
  | 'awaiting_payment'
  | 'payment_failed'
  | 'paid'
  | 'dispatched'
  | 'delivered'
  | 'cancelled'

export interface OrderLine {
  order_id: string
  product_id: string
  name: string
  quantity: number
  unit_price_cents: number
}

export interface Order {
  id: string
  tenant_id?: string
  customer_id: string
  status: OrderStatus
  total_cents: number
  currency: string
  shipping_method_code: string
  recipient_name: string
  address_line: string
  city: string
  country: string
  postal_code: string
  created_at: string
  updated_at: string
  lines?: OrderLine[]
}

export interface Payment {
  id: string
  order_id: string
  customer_id: string
  amount_cents: number
  currency: string
  status: string
  psp: string
  psp_ref: string
  brand: string
  last4: string
  failure_reason?: string
  created_at: string
  updated_at: string
}

export type ShipmentStatus =
  | 'pending'
  | 'dispatched'
  | 'in_transit'
  | 'delivered'
  | 'cancelled'

export interface Shipment {
  id: string
  order_id: string
  customer_id: string
  method_code: string
  dispatcher: string
  status: ShipmentStatus
  provider_ref: string
  tracking_url: string
  recipient_name: string
  address_line: string
  city: string
  country: string
  postal_code: string
  created_at: string
  updated_at: string
}

export interface ShippingMethod {
  id: string
  code: string
  name: string
  dispatcher: string
  is_default: boolean
  config: unknown
  created_at: string
  updated_at: string
}

export type Role = 'platform_admin' | 'tenant_admin' | 'staff' | 'customer'

export interface User {
  id: string
  tenant_id?: string
  email: string
  role: Role
  status: string
}

export interface TokenResponse {
  access_token: string
  refresh_token: string
  token_type: string
  expires_in: number
  user: User
}

export interface ApiErrorBody {
  error: { code: string; message: string }
}
