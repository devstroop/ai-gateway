import { Navigate, useLocation } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import type { ReactNode } from 'react'
import { fetchAdminSession } from '../lib/auth'

export default function RequireAuth({ children }: { children: ReactNode }) {
  const location = useLocation()
  const session = useQuery({
    queryKey: ['admin-session'],
    queryFn: fetchAdminSession,
  })

  if (session.isLoading) return <p>Loading…</p>
  if (session.isError) {
    return (
      <p>
        Failed to verify session.{' '}
        <button onClick={() => session.refetch()}>Retry</button>
      </p>
    )
  }
  if (!session.data) {
    return <Navigate to="/admin" state={{ from: location }} replace />
  }
  return <>{children}</>
}
