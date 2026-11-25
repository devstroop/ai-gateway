import { useState, useEffect, useRef, useMemo } from 'react'
import type { ChangeEvent } from 'react'
import ReactMarkdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import { useQuery } from '@tanstack/react-query'
import { Button } from '@astryxdesign/core/Button'
import { Card } from '@astryxdesign/core/Card'
import { Heading } from '@astryxdesign/core/Heading'
import { TextArea } from '@astryxdesign/core/TextArea'
import { Badge } from '@astryxdesign/core/Badge'
import { HStack } from '@astryxdesign/core/HStack'
import { VStack } from '@astryxdesign/core/VStack'
import { apiFetch } from '../lib/api'

type Model = { id: string }
type Msg = { role: 'user' | 'assistant' | 'system'; content: string }
type Thread = { id: string; title: string; messages: Msg[]; createdAt: number }

function loadThreads(): Thread[] {
  try {
    const raw = localStorage.getItem('chat-threads')
    if (!raw) return []
    const parsed = JSON.parse(raw)
    if (!Array.isArray(parsed)) return [newThread()]
    const valid = parsed.filter((t): t is Thread =>
      !!t && typeof t.id === 'string' && typeof t.title === 'string' &&
      Array.isArray(t.messages) && typeof t.createdAt === 'number' &&
      t.messages.every((m: unknown) => !!m && ((m as Msg).role === 'user' || (m as Msg).role === 'assistant' || (m as Msg).role === 'system') && typeof (m as Msg).content === 'string'),
    )
    return valid.length ? valid : [newThread()]
  } catch {
    return [newThread()]
  }
}

function genId(): string {
  if (typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function') {
    return crypto.randomUUID()
  }
  return 't-' + Math.random().toString(36).slice(2) + Date.now().toString(36)
}

function newThread(): Thread {
  return { id: genId(), title: 'New chat', messages: [], createdAt: Date.now() }
}

export default function Chat() {
  const { data: modelsData } = useQuery({
    queryKey: ['models'],
    queryFn: () => apiFetch<{ data: Model[] }>('/v1/models'),
  })
  const models = modelsData?.data ?? []
  const [model, setModel] = useState('')
  const initialThreads = useMemo(() => {
    const t = loadThreads()
    return t.length ? t : [newThread()]
  }, [])
  const [threads, setThreads] = useState<Thread[]>(initialThreads)
  const [currentId, setCurrentId] = useState<string>(() => {
    const saved = localStorage.getItem('chat-current')
    if (saved && initialThreads.some((x) => x.id === saved)) return saved
    return initialThreads[0].id
  })
  const [input, setInput] = useState('')
  const [search, setSearch] = useState('')
  const [streaming, setStreaming] = useState(false)
  const abortRef = useRef<AbortController | null>(null)
  const threadsRef = useRef<Thread[]>(threads)

  useEffect(() => {
    return () => {
      abortRef.current?.abort()
    }
  }, [])
  const [error, setError] = useState<string | null>(null)

  const current = threads.find((t) => t.id === currentId) ?? threads[0]
  const messages = current?.messages ?? []
  const activeId = threads.some((t) => t.id === currentId) ? currentId : (threads[0]?.id ?? currentId)

  threadsRef.current = threads

  useEffect(() => {
    const id = setTimeout(() => {
      try {
        localStorage.setItem('chat-threads', JSON.stringify(threads))
      } catch {
        // ignore storage errors
      }
    }, 250)
    return () => clearTimeout(id)
  }, [threads])
  useEffect(() => {
    return () => {
      try {
        localStorage.setItem('chat-threads', JSON.stringify(threadsRef.current))
      } catch {
        // ignore storage errors
      }
    }
  }, [])
  useEffect(() => {
    try {
      localStorage.setItem('chat-current', currentId)
    } catch {
      // ignore storage errors
    }
  }, [currentId])

  const selectedModel = model || models[0]?.id || ''

  useEffect(() => {
    if (threads.length && !threads.some((t) => t.id === currentId)) {
      setCurrentId(threads[0].id)
    }
  }, [threads, currentId])

  function updateMessages(updater: (prev: Msg[]) => Msg[], threadId: string = activeId) {
    setThreads((prev) =>
      prev.map((t) => (t.id === threadId ? { ...t, messages: updater(t.messages) } : t)),
    )
  }

  function updateTitle(firstUserMsg: string, threadId: string = activeId) {
    setThreads((prev) =>
      prev.map((t) =>
        t.id === threadId && t.title === 'New chat'
          ? { ...t, title: firstUserMsg.slice(0, 40) }
          : t,
      ),
    )
  }

  async function send() {
    const text = input.trim()
    if (!text || streaming) return
    setError(null)
    const originId = activeId
    const next: Msg[] = [...messages, { role: 'user', content: text }]
    updateMessages(() => next, originId)
    updateTitle(text, originId)
    setInput('')
    setStreaming(true)
    updateMessages((m) => [...m, { role: 'assistant', content: '' }], originId)

    try {
      const controller = new AbortController()
      abortRef.current = controller
      const base = import.meta.env.VITE_API_BASE ?? ''
      const res = await fetch((base || '') + '/v1/chat/completions', {
        method: 'POST',
        credentials: 'include',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ model: selectedModel, messages: next, stream: true }),
        signal: controller.signal,
      })
      if (!res.ok || !res.body) {
        const t = await res.text()
        throw new Error(t || `${res.status} ${res.statusText}`)
      }
      const reader = res.body.getReader()
      const decoder = new TextDecoder()
      let buf = ''
      let acc = ''
      while (true) {
        const { value, done } = await reader.read()
        if (done) break
        buf += decoder.decode(value, { stream: true })
        const lines = buf.split('\n')
        buf = lines.pop() ?? ''
        for (const line of lines) {
          const trimmed = line.trim()
          if (!trimmed.startsWith('data:')) continue
          const payload = trimmed.slice(5).trim()
          if (payload === '[DONE]') continue
          try {
            const json = JSON.parse(payload)
            const delta = json?.choices?.[0]?.delta?.content
            if (delta) {
              acc += delta
              updateMessages((m) => {
                const copy = [...m]
                const last = copy[copy.length - 1]
                if (last && last.role === 'assistant') last.content = acc
                return copy
              }, originId)
            }
          } catch {
            // ignore
          }
        }
      }
      // Process any trailing (non-newline-terminated) SSE event.
      if (buf.trim()) {
        for (const line of buf.split('\n')) {
          const trimmed = line.trim()
          if (!trimmed.startsWith('data:')) continue
          const payload = trimmed.slice(5).trim()
          if (payload === '[DONE]') continue
          try {
            const json = JSON.parse(payload)
            const delta = json?.choices?.[0]?.delta?.content
            if (delta) {
              acc += delta
              updateMessages((m) => {
                const copy = [...m]
                const last = copy[copy.length - 1]
                if (last && last.role === 'assistant') last.content = acc
                return copy
              }, originId)
            }
          } catch {
            // ignore
          }
        }
      }
    } catch (e) {
      if ((e as Error).name === 'AbortError') {
        setError('Stopped.')
      } else {
        setError((e as Error).message)
      }
      updateMessages((m) => {
        const copy = [...m]
        const last = copy[copy.length - 1]
        if (last && last.role === 'assistant' && last.content === '') copy.pop()
        return copy
      }, originId)
    } finally {
      abortRef.current = null
      setStreaming(false)
    }
  }

  function createNew() {
    const t = newThread()
    setThreads((prev) => [t, ...prev])
    setCurrentId(t.id)
    setError(null)
  }

  function removeThread(id: string) {
    const next = threads.filter((t) => t.id !== id)
    if (next.length === 0) {
      const t = newThread()
      setThreads([t])
      setCurrentId(t.id)
      return
    }
    setThreads(next)
    if (id === currentId) setCurrentId(next[0].id)
  }

  const filteredThreads = threads.filter((t) => {
    if (!search.trim()) return true
    const q = search.toLowerCase()
    return t.title.toLowerCase().includes(q) || t.messages.some((m) => m.content.toLowerCase().includes(q))
  })

  function renameThread(id: string) {
    const t = threads.find((x) => x.id === id)
    if (!t) return
    const name = window.prompt('Rename conversation', t.title)
    if (name && name.trim()) {
      setThreads((prev) => prev.map((x) => (x.id === id ? { ...x, title: name.trim() } : x)))
    }
  }

  function exportThreads() {
    const blob = new Blob([JSON.stringify(threads, null, 2)], { type: 'application/json' })
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = 'chat-threads.json'
    a.click()
    URL.revokeObjectURL(url)
  }

  function importThreads(e: ChangeEvent<HTMLInputElement>) {
    const file = e.target.files?.[0]
    if (!file) return
    const reader = new FileReader()
    reader.onload = () => {
      try {
        const parsed = JSON.parse(String(reader.result))
        if (Array.isArray(parsed)) {
          const seen = new Set(threadsRef.current.map((t) => t.id))
          const valid = parsed
            .filter((t): t is Thread =>
              !!t && typeof t.id === 'string' && typeof t.title === 'string' &&
              Array.isArray(t.messages) && typeof t.createdAt === 'number' &&
              t.messages.every((m: unknown) =>
                !!m && ((m as Msg).role === 'user' || (m as Msg).role === 'assistant' || (m as Msg).role === 'system') &&
                typeof (m as Msg).content === 'string',
              ),
            )
            .map((t) => {
              let id = t.id
              if (seen.has(id)) id = genId()
              seen.add(id)
              return { ...t, id }
            })
          if (valid.length === 0) {
            setError('Invalid import file.')
            return
          }
          const next = valid
          setThreads(next)
          setCurrentId(next[0].id)
          setError(null)
        } else {
          setError('Invalid import file.')
        }
      } catch {
        setError('Invalid import file.')
      }
    }
    reader.readAsText(file)
    e.target.value = ''
  }

  return (
    <HStack gap={0} style={{ minHeight: '60vh' }}>
      {/* Thread list */}
      <VStack gap={2} style={{ width: 220, borderRight: '1px solid var(--color-border, #e5e4e7)', paddingRight: '1rem' }}>
        <HStack justify="between" align="center">
          <Heading level={3}>Chats</Heading>
          <Button label="+" variant="ghost" onClick={createNew} />
        </HStack>
        <input
          value={search}
          onChange={(e) => setSearch(e.target.value)}
          placeholder="Search…"
          style={{ padding: '0.4rem 0.6rem', width: '100%', boxSizing: 'border-box' }}
        />
        <HStack gap={1}>
          <Button label="Export" variant="ghost" onClick={exportThreads} />
          <label style={{ cursor: 'pointer' }}>
            <span style={{ fontSize: 12, opacity: 0.7 }}>Import</span>
            <input type="file" accept="application/json" onChange={importThreads} style={{ display: 'none' }} />
          </label>
        </HStack>
        <VStack gap={1} style={{ maxHeight: 400, overflow: 'auto' }}>
          {filteredThreads.map((t) => (
            <HStack key={t.id} gap={1} align="center">
              <Button
                label={t.title}
                variant={t.id === currentId ? 'primary' : 'ghost'}
                onClick={() => setCurrentId(t.id)}
                style={{ flex: 1, textAlign: 'left' }}
              />
              <Button label="✎" variant="ghost" onClick={() => renameThread(t.id)} />
              <Button label="×" variant="ghost" onClick={() => removeThread(t.id)} />
            </HStack>
          ))}
        </VStack>
      </VStack>

      {/* Main chat */}
      <VStack gap={4} style={{ flex: 1, paddingLeft: '1rem' }}>
        <HStack justify="between" align="center">
          <Heading level={1}>Chat playground</Heading>
          <Badge label={streaming ? 'streaming…' : 'idle'} variant={streaming ? 'info' : 'neutral'} />
        </HStack>

        <HStack gap={3} align="center">
          <label htmlFor="model-select">Model</label>
          <select
            id="model-select"
            value={selectedModel}
            onChange={(e) => setModel(e.target.value)}
            style={{ padding: '0.5rem', minWidth: 240 }}
          >
            {models.length === 0 && <option value="">(no models)</option>}
            {models.map((m) => (
              <option key={m.id} value={m.id}>
                {m.id}
              </option>
            ))}
          </select>
        </HStack>

        <Card>
          <VStack gap={3} style={{ minHeight: 320, maxHeight: 480, overflow: 'auto' }}>
            {messages.length === 0 && <p>Start a conversation…</p>}
            {messages.map((m, i) => (
              <VStack key={i} gap={1} style={{ alignItems: m.role === 'user' ? 'flex-end' : 'flex-start' }}>
                <Badge label={m.role} variant={m.role === 'user' ? 'info' : 'neutral'} />
                <div
                  style={{
                    maxWidth: '80%',
                    padding: '0.75rem 1rem',
                    borderRadius: 8,
                    background: m.role === 'user' ? 'var(--color-surface-raised, #f4f3ec)' : 'transparent',
                    border: '1px solid var(--color-border, #e5e4e7)',
                  }}
                >
                  {m.content ? (
                    m.role === 'assistant' ? (
                      <ReactMarkdown remarkPlugins={[remarkGfm]}>{m.content}</ReactMarkdown>
                    ) : (
                      <span style={{ whiteSpace: 'pre-wrap' }}>{m.content}</span>
                    )
                  ) : (
                    streaming && m.role === 'assistant' ? '…' : ''
                  )}
                </div>
              </VStack>
            ))}
          </VStack>
        </Card>

        {error && <p style={{ color: 'red' }}>{error}</p>}

        <HStack gap={3} align="end">
          <div style={{ flex: 1 }}>
            <TextArea
              label="Message"
              value={input}
              onChange={(v) => setInput(v)}
              placeholder="Type a message…"
              onKeyDown={(e) => {
                if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) send()
              }}
            />
          </div>
          {streaming ? (
            <Button label="Stop" variant="secondary" onClick={() => abortRef.current?.abort()} />
          ) : (
            <Button label="Send" onClick={send} isDisabled={!input.trim()} />
          )}
        </HStack>
        <p style={{ fontSize: 12, opacity: 0.6 }}>Tip: Ctrl/Cmd+Enter to send.</p>
      </VStack>
    </HStack>
  )
}
