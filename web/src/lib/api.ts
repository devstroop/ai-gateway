const base = import.meta.env.VITE_API_BASE ?? ''

export async function apiFetch<T>(path: string, init?: RequestInit): Promise<T | undefined> {
  const { headers: initHeaders, ...rest } = init ?? {}
  const mergedHeaders = new Headers()
  if (initHeaders) {
    const h = new Headers(initHeaders)
    h.forEach((value, key) => mergedHeaders.set(key, value))
  }
  if (!mergedHeaders.has('content-type') && typeof rest.body === 'string') {
    try {
      const parsed = JSON.parse(rest.body)
      if (parsed !== null && typeof parsed === 'object') {
        mergedHeaders.set('content-type', 'application/json')
      }
    } catch {
      // leave content-type unset for non-JSON bodies
    }
  }
  const res = await fetch(base + path, {
    credentials: 'include',
    ...rest,
    headers: mergedHeaders,
  })
  if (!res.ok) {
    const text = await res.text()
    throw new Error(text || `${res.status} ${res.statusText}`)
  }
  const text = await res.text()
  if (!text) return undefined
  try {
    return JSON.parse(text) as T
  } catch {
    return text as unknown as T
  }
}
