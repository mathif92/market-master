import { Link, NavLink, Outlet, useNavigate } from 'react-router-dom'
import { ShoppingCart, Package, LayoutDashboard, LogOut, Store, User as UserIcon } from 'lucide-react'
import { useAuth } from '@/lib/auth'
import { useCart } from '@/lib/cart'
import { activeSlug, slugFromHost } from '@/lib/tenant'
import { Button } from '@/components/ui/button'
import { cn } from '@/lib/cn'

export function ShopLayout() {
  const { user, logout, isStaff } = useAuth()
  const { count } = useCart()
  const navigate = useNavigate()
  const hostSlug = slugFromHost()
  const slug = activeSlug()

  const navCls = ({ isActive }: { isActive: boolean }) =>
    cn(
      'inline-flex items-center gap-1.5 rounded-lg px-3 py-2 text-sm font-medium transition-colors',
      isActive ? 'bg-brand-50 text-brand-700' : 'text-slate-600 hover:bg-slate-100',
    )

  return (
    <div className="min-h-screen">
      <header className="sticky top-0 z-40 border-b border-slate-200 bg-white/90 backdrop-blur">
        <div className="mx-auto flex h-14 max-w-6xl items-center gap-4 px-4">
          <Link to="/" className="flex items-center gap-2 font-semibold text-slate-900">
            <span className="flex size-7 items-center justify-center rounded-lg bg-brand-600 text-white">
              <Store className="size-4" />
            </span>
            <span>{slug ? slug : 'Market'}</span>
            {hostSlug && (
              <span className="hidden rounded-full bg-brand-50 px-2 py-0.5 text-xs font-medium text-brand-700 sm:inline">
                {hostSlug}.localhost
              </span>
            )}
          </Link>

          <nav className="ml-2 flex items-center gap-1">
            <NavLink to="/" className={navCls} end>
              Shop
            </NavLink>
            {user && (
              <NavLink to="/orders" className={navCls}>
                <Package className="size-4" /> My orders
              </NavLink>
            )}
            {isStaff && (
              <NavLink to="/admin" className={navCls}>
                <LayoutDashboard className="size-4" /> Admin
              </NavLink>
            )}
          </nav>

          <div className="ml-auto flex items-center gap-2">
            <Link
              to="/cart"
              className="relative inline-flex items-center gap-2 rounded-lg px-3 py-2 text-sm font-medium text-slate-600 hover:bg-slate-100"
            >
              <ShoppingCart className="size-4" />
              <span className="hidden sm:inline">Cart</span>
              {count > 0 && (
                <span className="absolute -right-0.5 -top-0.5 flex size-5 items-center justify-center rounded-full bg-brand-600 text-[10px] font-bold text-white">
                  {count}
                </span>
              )}
            </Link>

            {user ? (
              <>
                <span className="hidden items-center gap-1.5 text-sm text-slate-600 sm:inline-flex">
                  <UserIcon className="size-4" />
                  {user.email}
                </span>
                <Button
                  variant="ghost"
                  size="sm"
                  onClick={() => {
                    logout()
                    navigate('/')
                  }}
                >
                  <LogOut className="size-4" /> Sign out
                </Button>
              </>
            ) : (
              <>
                <Button variant="ghost" size="sm" asChild>
                  <Link to="/login">Sign in</Link>
                </Button>
                <Button size="sm" asChild>
                  <Link to="/register">Create account</Link>
                </Button>
              </>
            )}
          </div>
        </div>
      </header>

      <main className="mx-auto max-w-6xl px-4 py-8">
        <Outlet />
      </main>

      <footer className="border-t border-slate-200 py-6 text-center text-xs text-slate-400">
        market-master · multi-tenant demo platform
      </footer>
    </div>
  )
}
