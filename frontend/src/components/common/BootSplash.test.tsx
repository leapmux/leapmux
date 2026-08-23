import { render, screen } from '@solidjs/testing-library'
import { describe, expect, it } from 'vitest'
import { BOOT_SPLASH_LABEL } from '~/lib/bootSplashTheme'
import { BootSplash } from './BootSplash'

describe('boot splash component', () => {
  it('renders the inline mark with the loading label and no external icon URL', () => {
    const { container } = render(() => <BootSplash />)
    expect(screen.getByTestId('boot-splash')).toBeInTheDocument()
    expect(screen.getByText(BOOT_SPLASH_LABEL)).toBeInTheDocument()
    expect(container.querySelector('img')).toBeNull()
    const icon = container.querySelector('[data-boot-splash-icon]')
    expect(icon?.tagName.toLowerCase()).toBe('svg')
    expect(icon?.getAttribute('viewBox')).toBe('0 0 64 64')
    expect(icon?.querySelector('rect')?.getAttribute('fill')).toBe('#0D9488')
    // Solid Suspense/AuthGuard splash must not reuse the static document id.
    expect(screen.getByTestId('boot-splash').id).toBe('')
  })
})
