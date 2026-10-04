import { useState, type FormEvent } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Plus } from 'lucide-react'
import { request, ApiError, newIdempotencyKey } from '@/lib/api'
import { useAuth } from '@/lib/auth'
import type { User } from '@/lib/types'
import { Button } from '@/components/ui/button'
import { Input, Field } from '@/components/ui/input'
import { Card, CardContent } from '@/components/ui/card'
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Badge } from '@/components/ui/badge'
import { Table, TBody, TD, TH, THead, TR } from '@/components/ui/table'
import { PageLoading, ErrorNote, Spinner } from '@/components/ui/feedback'

export function PlatformUsersPage() {
  const qc = useQueryClient()
  const { user: me } = useAuth()

  const { data, isLoading, error } = useQuery({
    queryKey: ['platform', 'users'],
    queryFn: () => request<{ users: User[] }>('/v1/platform/users'),
  })

  const [open, setOpen] = useState(false)
  const [form, setForm] = useState({ email: '', password: '' })
  const [formError, setFormError] = useState('')

  const create = useMutation({
    mutationFn: () =>
      request<User>('/v1/platform/users', {
        method: 'POST',
        idempotencyKey: newIdempotencyKey(),
        body: { email: form.email, password: form.password },
      }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['platform', 'users'] })
      setOpen(false)
      setForm({ email: '', password: '' })
      setFormError('')
    },
    onError: (err) =>
      setFormError(
        err instanceof ApiError
          ? err.code === 'email_taken'
            ? 'This email is already a platform user.'
            : err.message
          : 'Create failed',
      ),
  })

  const setStatus = useMutation({
    mutationFn: ({ id, status }: { id: string; status: 'active' | 'disabled' }) =>
      request<User>(`/v1/platform/users/${id}`, { method: 'PATCH', body: { status } }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['platform', 'users'] }),
    onError: (err) =>
      setFormError(err instanceof ApiError ? err.message : 'Status update failed'),
  })

  if (isLoading) return <PageLoading />
  if (error) return <ErrorNote message="Could not load platform users." />

  return (
    <div className="space-y-6">
      <div className="flex items-end justify-between">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">Platform users</h1>
          <p className="text-sm text-slate-500">
            Admins who can manage markets and invite other platform admins.
          </p>
        </div>
        <Button onClick={() => setOpen(true)}>
          <Plus className="size-4" /> Invite admin
        </Button>
      </div>

      {setStatus.error && (
        <ErrorNote
          message={setStatus.error instanceof ApiError ? setStatus.error.message : 'Update failed'}
        />
      )}

      <Card>
        <CardContent className="p-0">
          <Table>
            <THead>
              <tr>
                <TH>Email</TH>
                <TH>Status</TH>
                <TH>Created</TH>
                <TH className="text-right">Actions</TH>
              </tr>
            </THead>
            <TBody>
              {(data?.users ?? []).map((u) => {
                const isSelf = u.id === me?.id
                return (
                  <TR key={u.id}>
                    <TD className="font-medium">
                      {u.email}
                      {isSelf && <span className="ml-2 text-xs text-slate-400">(you)</span>}
                    </TD>
                    <TD>
                      <Badge variant={u.status === 'active' ? 'success' : 'danger'}>
                        {u.status}
                      </Badge>
                    </TD>
                    <TD className="text-slate-500">
                      {u.created_at
                        ? new Date(u.created_at).toLocaleDateString(undefined, {
                            year: 'numeric',
                            month: 'short',
                            day: 'numeric',
                          })
                        : '—'}
                    </TD>
                    <TD>
                      <div className="flex justify-end">
                        <Button
                          variant={u.status === 'active' ? 'outline' : 'default'}
                          size="sm"
                          disabled={isSelf || setStatus.isPending}
                          title={isSelf ? 'You cannot disable your own account' : undefined}
                          onClick={() =>
                            setStatus.mutate({
                              id: u.id,
                              status: u.status === 'active' ? 'disabled' : 'active',
                            })
                          }
                        >
                          {setStatus.isPending ? <Spinner /> : null}
                          {u.status === 'active' ? 'Disable' : 'Enable'}
                        </Button>
                      </div>
                    </TD>
                  </TR>
                )
              })}
            </TBody>
          </Table>
        </CardContent>
      </Card>

      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Invite platform admin</DialogTitle>
          </DialogHeader>
          <form
            className="space-y-4"
            onSubmit={(e: FormEvent) => {
              e.preventDefault()
              create.mutate()
            }}
          >
            <Field label="Email" htmlFor="pu-email">
              <Input
                id="pu-email"
                type="email"
                required
                value={form.email}
                onChange={(e) => setForm({ ...form, email: e.target.value })}
                placeholder="ops@example.com"
                autoComplete="email"
              />
            </Field>
            <Field label="Password" htmlFor="pu-pass" hint="At least 8 characters.">
              <Input
                id="pu-pass"
                type="password"
                required
                minLength={8}
                value={form.password}
                onChange={(e) => setForm({ ...form, password: e.target.value })}
                autoComplete="new-password"
              />
            </Field>
            {formError && <ErrorNote message={formError} />}
            <DialogFooter>
              <Button type="button" variant="outline" onClick={() => setOpen(false)}>
                Cancel
              </Button>
              <Button type="submit" disabled={create.isPending}>
                {create.isPending && <Spinner />} Invite
              </Button>
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>
    </div>
  )
}
