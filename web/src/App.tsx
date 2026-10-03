import { Navigate, Route, Routes, useLocation } from 'react-router-dom'
import { useAuth } from './lib/auth'
import { ShopLayout } from './layouts/ShopLayout'
import { AdminLayout } from './layouts/AdminLayout'
import { LoginPage } from './pages/auth/LoginPage'
import { RegisterPage } from './pages/auth/RegisterPage'
import { CatalogPage } from './pages/shop/CatalogPage'
import { ProductPage } from './pages/shop/ProductPage'
import { CartPage } from './pages/shop/CartPage'
import { CheckoutPage } from './pages/shop/CheckoutPage'
import { MyOrdersPage } from './pages/shop/MyOrdersPage'
import { OrderPage } from './pages/shop/OrderPage'
import { DashboardPage } from './pages/admin/DashboardPage'
import { AdminCatalogPage } from './pages/admin/AdminCatalogPage'
import { AdminOrdersPage } from './pages/admin/AdminOrdersPage'
import { AdminOrderDetailPage } from './pages/admin/AdminOrderDetailPage'
import { AdminShipmentsPage } from './pages/admin/AdminShipmentsPage'
import { AdminShippingMethodsPage } from './pages/admin/AdminShippingMethodsPage'
import { AdminUsersPage } from './pages/admin/AdminUsersPage'

function RequireAuth({ children, staff }: { children: React.ReactNode; staff?: boolean }) {
  const { user, loading, isStaff } = useAuth()
  const location = useLocation()
  if (loading) return null
  if (!user) return <Navigate to="/login" state={{ from: location.pathname }} replace />
  if (staff && !isStaff) return <Navigate to="/" replace />
  return <>{children}</>
}

export default function App() {
  return (
    <Routes>
      <Route element={<ShopLayout />}>
        <Route path="/" element={<CatalogPage />} />
        <Route path="/products/:id" element={<ProductPage />} />
        <Route path="/cart" element={<CartPage />} />
        <Route path="/login" element={<LoginPage />} />
        <Route path="/register" element={<RegisterPage />} />
        <Route
          path="/checkout"
          element={
            <RequireAuth>
              <CheckoutPage />
            </RequireAuth>
          }
        />
        <Route
          path="/orders"
          element={
            <RequireAuth>
              <MyOrdersPage />
            </RequireAuth>
          }
        />
        <Route
          path="/orders/:id"
          element={
            <RequireAuth>
              <OrderPage />
            </RequireAuth>
          }
        />
      </Route>

      <Route
        path="/admin"
        element={
          <RequireAuth staff>
            <AdminLayout />
          </RequireAuth>
        }
      >
        <Route index element={<DashboardPage />} />
        <Route path="catalog" element={<AdminCatalogPage />} />
        <Route path="orders" element={<AdminOrdersPage />} />
        <Route path="orders/:id" element={<AdminOrderDetailPage />} />
        <Route path="shipments" element={<AdminShipmentsPage />} />
        <Route path="shipping-methods" element={<AdminShippingMethodsPage />} />
        <Route path="users" element={<AdminUsersPage />} />
      </Route>

      <Route path="*" element={<Navigate to="/" replace />} />
    </Routes>
  )
}
