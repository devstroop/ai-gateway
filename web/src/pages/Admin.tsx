import { useState, useEffect } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { Button } from '@astryxdesign/core/Button'
import { Card } from '@astryxdesign/core/Card'
import { Heading } from '@astryxdesign/core/Heading'
import { Badge } from '@astryxdesign/core/Badge'
import { Spinner } from '@astryxdesign/core/Spinner'
import { TextInput } from '@astryxdesign/core/TextInput'
import { VStack } from '@astryxdesign/core/VStack'
import { useToast } from '@astryxdesign/core/Toast'
import { HStack } from '@astryxdesign/core/HStack'
import { apiFetch } from '../lib/api'
import { fetchAdminSession } from '../lib/auth'


export default function Admin() {
  const qc = useQueryClient()
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [csrf, setCsrf] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)
  const showToast = useToast()
  const [defaultModel, setDefaultModel] = useState('')
  const [runtimeDirty, setRuntimeDirty] = useState(false)
  const [newUsername, setNewUsername] = useState('')
  const [newPassword, setNewPassword] = useState('')
  const [newRole, setNewRole] = useState('user')

  const setupStatus = useQuery({
    queryKey: ['setup-status'],
    queryFn: () => apiFetch<{ setup_required: boolean }>('/api/admin/setup/status'),
  })

  const session = useQuery({
    queryKey: ['admin-session'],
    queryFn: fetchAdminSession,
  })
  const loggedIn = !!session.data

  const runtime = useQuery({
    queryKey: ['runtime'],
    queryFn: () => apiFetch<{ default_model: string; found: boolean }>('/api/admin/runtime'),
    enabled: loggedIn,
  })

  const users = useQuery({
    queryKey: ['admin-users'],
    queryFn: () => apiFetch<{ users: { username: string; role: string }[] }>('/api/admin/users'),
    enabled: loggedIn,
  })

  const createUser = useMutation({
    mutationFn: () =>
      apiFetch<void>('/api/admin/users', {
        method: 'POST',
        headers: { 'X-CSRF-Token': csrf ?? '' },
        body: JSON.stringify({ username: newUsername, password: newPassword, role: newRole }),
      }),
    onSuccess: () => {
      setNewUsername('')
      setNewPassword('')
      setNewRole('user')
      setError(null)
      showToast({ body: 'User created.' })
      qc.invalidateQueries({ queryKey: ['admin-users'] })
    },
    onError: (e: Error) => setError(e.message),
  })

  const saveRuntime = useMutation({
    mutationFn: (value: string) =>
      apiFetch<void>('/api/admin/runtime', {
        method: 'PUT',
        headers: { 'X-CSRF-Token': csrf ?? '' },
        body: JSON.stringify({ default_model: value }),
      }),
    onSuccess: () => { setRuntimeDirty(false); showToast({ body: 'Runtime settings saved.' }); qc.invalidateQueries({ queryKey: ['runtime'] }) },
    onError: (e: Error) => setError(e.message),
  })

  const setupMut = useMutation({
    mutationFn: () =>
      apiFetch<{ csrf_token: string }>('/api/admin/setup', {
        method: 'POST',
        body: JSON.stringify({ username, password }),
      }),
    onSuccess: (data) => {
      setCsrf(data?.csrf_token ?? null)
      setError(null)
      showToast({ body: 'Setup complete: admin account created.' })
      qc.invalidateQueries({ queryKey: ['admin-session'] })
      qc.invalidateQueries({ queryKey: ['setup-status'] })
    },
    onError: (e: Error) => setError(e.message),
  })

  const loginMut = useMutation({
    mutationFn: () =>
      apiFetch<{ csrf_token: string; username: string; role: string }>('/api/admin/auth/login', {
        method: 'POST',
        body: JSON.stringify({ username, password }),
      }),
    onSuccess: (data) => {
      setCsrf(data?.csrf_token ?? null)
      setError(null)
      showToast({ body: `Signed in as ${data?.username ?? username}.` })
      qc.invalidateQueries({ queryKey: ['admin-session'] })
    },
    onError: (e: Error) => setError(e.message),
  })

  const logoutMut = useMutation({
    mutationFn: () =>
      apiFetch<void>('/api/admin/auth/logout', {
        method: 'POST',
        headers: { 'X-CSRF-Token': csrf ?? '' },
      }),
    onSuccess: () => {
      setCsrf(null)
      setRuntimeDirty(false)
      setDefaultModel('')
      showToast({ body: 'Signed out.' })
      qc.removeQueries({ queryKey: ['admin-session'] })
      qc.invalidateQueries({ queryKey: ['admin-session'] })
    },
    onError: (e: Error) => setError(e.message),
  })


  useEffect(() => {
    if (session.data?.csrf_token) setCsrf(session.data.csrf_token)
  }, [session.data])
  useEffect(() => {
    if (runtime.data && !runtimeDirty) setDefaultModel(runtime.data.default_model ?? '')
  }, [runtime.data, runtimeDirty])

  useEffect(() => {
    if (!loggedIn) {
      setRuntimeDirty(false)
      setDefaultModel('')
    }
  }, [loggedIn])

  if (setupStatus.isLoading) return <p>Loading…</p>

  return (
    <VStack gap={6}>
      <HStack justify="between" align="center">
        <Heading level={1}>Admin</Heading>
        {loggedIn && session.data && (
          <Badge label={`Signed in as ${session.data.username}`} variant="success" />
        )}
      </HStack>

      {error && <p style={{ color: 'red' }}>{error}</p>}

      {setupStatus.data?.setup_required && !loggedIn && (
        <Card>
          <VStack gap={3}>
            <Heading level={2}>First-run setup</Heading>
            <TextInput
              label="Username"
              value={username}
              onChange={(v) => setUsername(v)}
            />
            <TextInput
              label="Password"
              type="password"
              value={password}
              onChange={(v) => setPassword(v)}
            />
            <Button label="Create admin" onClick={() => setupMut.mutate()} isDisabled={!username || !password || setupMut.isPending} />
          </VStack>
        </Card>
      )}

      {!setupStatus.data?.setup_required && !loggedIn && (
        <Card>
          <VStack gap={3}>
            <Heading level={2}>Sign in</Heading>
            <TextInput
              label="Username"
              value={username}
              onChange={(v) => setUsername(v)}
            />
            <TextInput
              label="Password"
              type="password"
              value={password}
              onChange={(v) => setPassword(v)}
            />
            <Button label="Sign in" onClick={() => loginMut.mutate()} isDisabled={!username || !password || loginMut.isPending} />
          </VStack>
        </Card>
      )}

      {loggedIn && (
        <Card>
          <VStack gap={3}>
            <Heading level={2}>Session</Heading>
            <p>Username: {session.data?.username}</p>
            <p>CSRF token: {csrf ?? '—'}</p>
            <Button label="Sign out" variant="secondary" onClick={() => logoutMut.mutate()} />
          </VStack>
        </Card>
      )}

      {loggedIn && (
        <Card>
          <VStack gap={3}>
            <Heading level={2}>Runtime settings</Heading>
            <TextInput label="Default model" value={defaultModel} onChange={(v) => { setDefaultModel(v); setRuntimeDirty(true) }} />
            <HStack gap={3}>
              <Button label="Save" onClick={() => saveRuntime.mutate(defaultModel)} isDisabled={saveRuntime.isPending} />
              <Button label="Clear" variant="secondary" onClick={() => saveRuntime.mutate('')} isDisabled={saveRuntime.isPending} />
            </HStack>
            {runtime.data?.found === false && <p>Not set (using config default).</p>}
          </VStack>
        </Card>
      )}

      {loggedIn && (
        <Card>
          <VStack gap={3}>
            <Heading level={2}>Users</Heading>
            <VStack gap={2}>
              {users.isLoading && <Spinner />}
              {(users.data?.users ?? []).map((u) => (
                <HStack key={u.username} justify="between" align="center">
                  <span>{u.username}</span>
                  <Badge label={u.role} variant="neutral" />
                </HStack>
              ))}
              {(users.data?.users ?? []).length === 0 && <p>No users.</p>}
            </VStack>
            <Heading level={3}>Create user</Heading>
            <TextInput label="Username" value={newUsername} onChange={(v) => setNewUsername(v)} />
            <TextInput label="Password" type="password" value={newPassword} onChange={(v) => setNewPassword(v)} />
            <select value={newRole} onChange={(e) => setNewRole(e.target.value)} style={{ padding: '0.5rem' }}>
              <option value="user">user</option>
              <option value="admin">admin</option>
            </select>
            <Button label="Create user" onClick={() => createUser.mutate()} isDisabled={!newUsername || !newPassword || createUser.isPending} />
          </VStack>
        </Card>
      )}
    </VStack>
  )
}
