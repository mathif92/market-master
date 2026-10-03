import { useState, type FormEvent } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { request, ApiError } from '@/lib/api'
import { useAuth } from '@/lib/auth'
import type { Role, User } from '@/lib/types'
import { Button } from '@/components/ui/button'
import { Input, Field } from '@/components/ui/input'
import { Card, CardContent } from '@/components/ui/card'
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Badge } from '@/components/ui/badge'
import { Table, TBody, TD, TH, THead, TR } from '@/components/ui/table'
import { PageLoading, ErrorNote, Spinner } from '@/components/ui/feedback'
import { Plus } from 'lucide-react'

const ROLE_LABEL: Record<Role, string> = {
  platform_admin: 'Platform admin',
  tenant_admin: 'Market admin',
  staff: 'Staff',
  customer: 'Customer',
}

export function AdminUsersPage() {
  const qc = useQueryClient()
  const { user: me } = useAuth()
  const isAdmin = me?.role === 'tenant_admin' || me?.role === 'platform_admin'

  const { data, isLoading, error } = useQuery({
    queryKey: ['users'],
    queryFn: () => request<{ users: User[] }>('/v1/users'),
  })

  const [open, setOpen] = useState(false)
  const [form, setForm] = useState({ email: '', password: '', role: 'staff' })
  const [formError, setFormError] = useState('')

  const create = useMutation({
    mutationFn: () =>
      request<User>('/v1/users', {
        method: 'POST',
        body: { email: form.email, password: form.password, role: form.role },
      }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['users'] })
      setOpen(false)
      setForm({ email: '', password: '', role: 'staff' })
      setFormError('')
    },
    onError: (err) => setFormError(err instanceof ApiError ? err.message : 'Create failed'),
  })

  if (isLoading) return <PageLoading />
  if (error) return <ErrorNote message="Could not load users." />

  return (
    <div className="space-y-6">
      <div className="flex items-end justify-between">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">Users</h1>
          <p className="text-sm text-slate-500">People with access to this market.</p>
        </div>
        {isAdmin && (
          <Button onClick={() => setOpen(true)}>
            <Plus className="size-4" /> New user
          </Button>
        )}
      </div>

      <Card>
        <CardContent className="p-0">
          <Table>
            <THead>
              <tr>
                <TH>Email</TH>
                <TH>Role</TH>
                <TH>Status</TH>
              </tr>
            </THead>
            <TBody>
              {(data?.users ?? []).map((u) => (
                <TR key={u.id}>
                  <TD className="font-medium">
                    {u.email}
                    {u.id === me?.id && (
                      <span className="ml-2 text-xs text-slate-400">(you)</span>
                    )}
                  </TD>
                  <TD>
                    <Badge variant={u.role === 'tenant_admin' ? 'brand' : 'default'}>
                      {ROLE_LABEL[u.role] ?? u.role}
                    </Badge>
                  </TD>
                  <TD className="capitalize text-slate-500">{u.status}</TD>
                </TR>
              ))}
            </TBody>
          </Table>
        </CardContent>
      </Card>

      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>New user</DialogTitle>
          </DialogHeader>
          <form
            className="space-y-4"
            onSubmit={(e: FormEvent) => {
              e.preventDefault()
              create.mutate()
            }}
          >
            <Field label="Email" htmlFor="u-email">
              <Input
                id="u-email"
                type="email"
                required
                value={form.email}
                onChange={(e) => setForm({ ...form, email: e.target.value })}
              />
            </Field>
            <Field label="Password" htmlFor="u-pass" hint="At least 8 characters.">
              <Input
                id="u-pass"
                type="password"
                required
                minLength={8}
                value={form.password}
                onChange={(e) => setForm({ ...form, password: e.target.value })}
              />
            </Field>
            <Field label="Role" htmlFor="u-role">
              <Select value={form.role} onValueChange={(v) => setForm({ ...form, role: v })}>
                <SelectTrigger id="u-role">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="staff">Staff</SelectItem>
                  <SelectItem value="tenant_admin">Market admin</SelectItem>
                  <SelectItem value="customer">Customer</SelectItem>
                </SelectContent>
              </Select>
            </Field>
            {formError && <ErrorNote message={formError} />}
            <DialogFooter>
              <Button type="button" variant="outline" onClick={() => setOpen(false)}>
                Cancel
              </Button>
              <Button type="submit" disabled={create.isPending}>
                {create.isPending && <Spinner />} Create
              </Button>
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>
    </div>
  )
}
