import { Link, NavLink, Outlet, useNavigate } from 'react-router-dom'
import { ArrowLeft, Globe, LogOut, ShieldCheck } from 'lucide-react'
import { useAuth } from '@/lib/auth'
import { cn } from '@/lib/cn'

const NAV = [
  { to: '/platform', end: true, label: 'Markets', icon: Globe },
  { to: '/platform/users', label: 'Platform users', icon: ShieldCheck },
]

export function PlatformLayout() {
  const { user, logout } = useAuth()
  const navigate = useNavigate()

  const navCls = ({ isActive }: { isActive: boolean }) =>
    cn(
      'flex items-center gap-2.5 rounded-lg px-3 py-2 text-sm font-medium transition-colors',
      isActive
        ? 'bg-white text-brand-700 shadow-sm'
        : 'text-slate-300 hover:bg-slate-800 hover:text-white',
    )

  return (
    <div className="flex min-h-screen bg-canvas">
      <aside className="flex w-60 shrink-0 flex-col border-r border-slate-800 bg-slate-900">
        <div className="flex h-14 items-center gap-2 border-b border-slate-800 px-4">
          <span className="flex size-7 items-center justify-center rounded-lg bg-brand-500 text-white">
            <Globe className="size-4" />
          </span>
          <span className="font-semibold text-white">Platform</span>
        </div>

        <nav className="flex flex-1 flex-col gap-1 p-3">
          {NAV.map(({ to, end, label, icon: Icon }) => (
            <NavLink key={to} to={to} end={end} className={navCls}>
              <Icon className="size-4" />
              {label}
            </NavLink>
          ))}
        </nav>

        <div className="border-t border-slate-800 p-3">
          <p className="truncate px-3 pb-2 text-xs text-slate-400">{user?.email}</p>
          <div className="flex flex-col gap-1">
            <Link
              to="/"
              className="flex items-center gap-2.5 rounded-lg px-3 py-2 text-sm font-medium text-slate-300 hover:bg-slate-800 hover:text-white"
            >
              <ArrowLeft className="size-4" /> Back to shop
            </Link>
            <button
              onClick={() => {
                logout()
                navigate('/')
              }}
              className="flex items-center gap-2.5 rounded-lg px-3 py-2 text-sm font-medium text-slate-300 hover:bg-slate-800 hover:text-white"
            >
              <LogOut className="size-4" /> Sign out
            </button>
          </div>
        </div>
      </aside>

      <main className="flex-1 overflow-x-hidden">
        <div className="mx-auto max-w-5xl px-6 py-8">
          <Outlet />
        </div>
      </main>
    </div>
  )
}
