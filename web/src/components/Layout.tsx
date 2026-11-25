import { NavLink, Outlet } from 'react-router-dom'
import { HStack } from '@astryxdesign/core/HStack'
import { VStack } from '@astryxdesign/core/VStack'
import { Button } from '@astryxdesign/core/Button'
import { Heading } from '@astryxdesign/core/Heading'
import { useThemeMode } from '../lib/themeMode'

const nav = [
  { to: '/', label: 'Dashboard' },
  { to: '/chat', label: 'Chat' },
  { to: '/admin', label: 'Admin' },
]

export default function Layout() {
  return (
    <HStack gap={0} style={{ minHeight: '100vh' }}>
      <VStack
        gap={2}
        style={{
          width: 220,
          padding: '1.5rem 1rem',
          borderRight: '1px solid var(--color-border, #e5e4e7)',
        }}
      >
        <Heading level={2}>AI Gateway</Heading>
        {nav.map((n) => (
          <NavLink key={n.to} to={n.to} style={{ textDecoration: 'none' }}>
            {({ isActive }) => (
              <Button
                label={n.label}
                variant={isActive ? 'primary' : 'ghost'}
                width="full"
              />
            )}
          </NavLink>
        ))}
        <div style={{ marginTop: 'auto', paddingTop: '1rem' }}>
          <ThemeToggle />
        </div>
      </VStack>
      <VStack gap={4} style={{ flex: 1, padding: '2rem' }}>
        <Outlet />
      </VStack>
    </HStack>
  )
}


function ThemeToggle() {
  const { mode, setMode } = useThemeMode()
  return (
    <Button
      label={`Theme: ${mode}`}
      variant="ghost"
      onClick={() => setMode(mode === 'light' ? 'dark' : mode === 'dark' ? 'system' : 'light')}
    />
  )
}
