import { createContext, useContext, useEffect, useState, type ReactNode } from 'react'

type Mode = 'light' | 'dark' | 'system'

const Ctx = createContext<{ mode: Mode; setMode: (m: Mode) => void } | null>(null)

export function ThemeModeProvider({ children }: { children: ReactNode }) {
  const [mode, setMode] = useState<Mode>(() => {
    try {
      const saved = localStorage.getItem('theme-mode')
      return (saved === 'light' || saved === 'dark' || saved === 'system') ? saved : 'system'
    } catch {
      return 'system'
    }
  })
  useEffect(() => {
    try {
      localStorage.setItem('theme-mode', mode)
    } catch {
      // ignore storage errors
    }
  }, [mode])
  return <Ctx.Provider value={{ mode, setMode }}>{children}</Ctx.Provider>
}

export function useThemeMode() {
  const v = useContext(Ctx)
  if (!v) throw new Error('useThemeMode must be used within ThemeModeProvider')
  return v
}
