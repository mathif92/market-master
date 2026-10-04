export interface Category {
  id: string
  parent_id?: string
  name: string
  slug: string
  created_at: string
  updated_at: string
}

/** Live non-code campaign annotation attached to product reads. */
export interface ProductCampaign {
  id: string
  name: string
  rule_type: 'percent' | 'fixed'
  rule_value: number
  ends_at: string
}

export interface Product {
  id: string
  category_id?: string
  name: string
  slug: string
  description: string
  /** Canonical catalog price — never rewritten by campaigns. */
  price_cents: number
  /** Server-computed sale price while a live campaign covers this product. */
  sale_price_cents?: number
  campaign?: ProductCampaign
  currency: string
  status: 'active' | 'archived'
  created_at: string
  updated_at: string
}

/** Stock state returned by GET /v1/products/{id} (null = untracked). */
export interface StockLevel {
  product_id: string
  product_slug: string
  product_name: string
  tracked: boolean
  on_hand: number
  reserved: number
  available: number
  low_stock_threshold: number
  updated_at?: string
}

export interface ProductWithStock extends Product {
  stock: StockLevel | null
}

export interface StockMovement {
  id: string
  product_id: string
  product_name: string
  source: string
  kind: string
  qty_delta: number
  on_hand_after: number
  batch_key?: string
  ref_id?: string
  created_at: string
}

export interface IngestRowError {
  row: number
  sku: string
  error: string
}

export interface IngestResult {
  batch_id: string
  replayed: boolean
  mode: string
  total_rows: number
  applied_rows: number
  error_rows: number
  errors: IngestRowError[]
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
  /** Final unit price after the applied campaign. */
  unit_price_cents: number
  /** Canonical pre-discount price (equals unit_price_cents without a campaign). */
  list_price_cents: number
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
  created_at?: string
}

export interface Tenant {
  id: string
  slug: string
  name: string
  status: 'active' | 'suspended'
  created_at: string
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

/** Admin campaign (discount) as returned by /v1/campaigns. */
export interface Campaign {
  id: string
  name: string
  status: 'draft' | 'active' | 'archived'
  rule_type: 'percent' | 'fixed'
  /** percent: basis points (10000 = 100%); fixed: cents off per unit. */
  rule_value: number
  scope_type: 'sitewide' | 'category' | 'product'
  scope_id?: string
  requires_code: boolean
  starts_at: string
  ends_at: string
  max_redemptions?: number
  redemptions_count: number
  created_at: string
  updated_at: string
}

export interface CampaignCode {
  id: string
  campaign_id: string
  code: string
  max_uses?: number
  uses_count: number
  created_at: string
}
