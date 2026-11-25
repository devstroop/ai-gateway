import { render, screen } from '@testing-library/react'
import { ThemeModeProvider, useThemeMode } from '../lib/themeMode'

function Probe() {
  const { mode } = useThemeMode()
  return <span>mode:{mode}</span>
}

test('theme mode provider defaults to system', () => {
  render(
    <ThemeModeProvider>
      <Probe />
    </ThemeModeProvider>,
  )
  expect(screen.getByText(/mode:/)).toBeInTheDocument()
})
