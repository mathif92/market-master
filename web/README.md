# web — React SPA

Customer storefront (`/`) and market admin (`/admin`) for market-master.

- **Stack**: Vite + React 19 + TypeScript (strict) + Tailwind CSS v4 +
  TanStack Query + react-router v7; shadcn-style components on Radix UI
  (`src/components/ui/`), icons from lucide-react.
- **API**: all calls go through `src/lib/api.ts` — attaches the market slug
  (`X-Tenant-Slug` fallback for host-based resolution) and bearer token,
  transparently refreshes expired access tokens, maps
  `{"error":{code,message}}` to `ApiError`, and generates `Idempotency-Key`
  headers for mutating calls.
- **Auth**: session (access/refresh tokens + user) in `localStorage`
  (`src/lib/session.ts`), React context in `src/lib/auth.tsx`.
- **Cart**: client-side only (`src/lib/cart.tsx`, localStorage) — the
  backend has no cart resource; checkout posts real order lines.

## Commands

```bash
npm install      # first time
npm run dev      # :5173, /v1 proxied to gateway :8080
npm run build    # strict typecheck + production bundle → dist/
npm run lint     # oxlint
```

Or from the repo root: `make web-dev`, `make web-build`, `make web-lint`.

## Layout

```
src/lib/         api client, auth, cart, tenant resolution, types, format
src/components/  ui kit (button, dialog, select, table, badge, …)
src/layouts/     ShopLayout (header), AdminLayout (sidebar)
src/pages/auth/  login, register
src/pages/shop/  catalog, product, cart, checkout, orders
src/pages/admin/ dashboard, catalog, orders, shipments, methods, users
```

Market resolution: hostname subdomain first (`demo.localhost`), else the
stored slug (`lib/tenant.ts`). The gateway does the same server-side.
