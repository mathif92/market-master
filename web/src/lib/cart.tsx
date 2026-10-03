import { createContext, useContext, useEffect, useMemo, useState } from 'react'

export interface CartItem {
  productId: string
  name: string
  unitPriceCents: number
  currency: string
  quantity: number
}

interface CartState {
  items: CartItem[]
  add(item: Omit<CartItem, 'quantity'>, qty?: number): void
  setQuantity(productId: string, qty: number): void
  remove(productId: string): void
  clear(): void
  count: number
  totalCents: number
}

const CartContext = createContext<CartState | null>(null)
const KEY = 'market.cart.v1'

function load(): CartItem[] {
  try {
    return JSON.parse(localStorage.getItem(KEY) ?? '[]') as CartItem[]
  } catch {
    return []
  }
}

export function CartProvider({ children }: { children: React.ReactNode }) {
  const [items, setItems] = useState<CartItem[]>(load)

  useEffect(() => {
    localStorage.setItem(KEY, JSON.stringify(items))
  }, [items])

  const value = useMemo<CartState>(() => {
    const add: CartState['add'] = (item, qty = 1) =>
      setItems((prev) => {
        const existing = prev.find((i) => i.productId === item.productId)
        if (existing) {
          return prev.map((i) =>
            i.productId === item.productId ? { ...i, quantity: i.quantity + qty } : i,
          )
        }
        return [...prev, { ...item, quantity: qty }]
      })

    return {
      items,
      add,
      setQuantity: (productId, qty) =>
        setItems((prev) =>
          qty <= 0
            ? prev.filter((i) => i.productId !== productId)
            : prev.map((i) => (i.productId === productId ? { ...i, quantity: qty } : i)),
        ),
      remove: (productId) => setItems((prev) => prev.filter((i) => i.productId !== productId)),
      clear: () => setItems([]),
      count: items.reduce((n, i) => n + i.quantity, 0),
      totalCents: items.reduce((n, i) => n + i.quantity * i.unitPriceCents, 0),
    }
  }, [items])

  return <CartContext.Provider value={value}>{children}</CartContext.Provider>
}

export function useCart(): CartState {
  const ctx = useContext(CartContext)
  if (!ctx) throw new Error('useCart must be used inside <CartProvider>')
  return ctx
}
