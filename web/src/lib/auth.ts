export type AdminSession = { username: string; csrf_token?: string }

export async function fetchAdminSession(): Promise<AdminSession | null> {
  const base = import.meta.env.VITE_API_BASE ?? ''
  const res = await fetch((base || '') + '/api/admin/auth/session', { credentials: 'include' })
  if (res.status === 401) return null
  if (!res.ok) throw new Error(`${res.status} ${res.statusText}`)
  return res.json() as Promise<AdminSession>
}
