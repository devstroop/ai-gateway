import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { BrowserRouter, Routes, Route } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { Theme } from '@astryxdesign/core/theme'
import { LayerProvider } from '@astryxdesign/core/Layer'
import { themes, defaultThemeSlug } from './astryx-themes'
import '@astryxdesign/core/reset.css'
import '@astryxdesign/core/astryx.css'
import { ThemeModeProvider, useThemeMode } from './lib/themeMode'
import Layout from './components/Layout'
import Dashboard from './pages/Dashboard'
import Chat from './pages/Chat'
import Admin from './pages/Admin'
import RequireAuth from './components/RequireAuth'

const queryClient = new QueryClient()

function ThemedApp() {
  const { mode } = useThemeMode()
  return (
    <Theme theme={themes[defaultThemeSlug]} mode={mode}>
      <LayerProvider>
        <BrowserRouter>
        <Routes>
          <Route element={<Layout />}>
            <Route index element={<RequireAuth><Dashboard /></RequireAuth>} />
            <Route path="chat" element={<RequireAuth><Chat /></RequireAuth>} />
            <Route path="admin" element={<Admin />} />
          </Route>
        </Routes>
      </BrowserRouter>
      </LayerProvider>
    </Theme>
  )
}

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <ThemeModeProvider>
        <ThemedApp />
      </ThemeModeProvider>
    </QueryClientProvider>
  </StrictMode>,
)
