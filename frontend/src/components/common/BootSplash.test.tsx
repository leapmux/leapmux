import { render, screen } from '@solidjs/testing-library'
import { describe, expect, it } from 'vitest'
import { BOOT_SPLASH_LABEL, BOOT_SPLASH_PHASES } from '~/lib/bootSplashTheme'
import { BootSplash, BootSplashProgress } from './BootSplash'

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

  // The checklist is the second of the two trees that must stay one design
  // (the static one is covered in `bootSplashTheme.test.ts`). Rows come from
  // the shared phase array, and state changes only via CSS on the phase
  // attribute — the DOM here is identical in both trees.
  it('renders every checklist row from the shared phases, in order', () => {
    const { container } = render(() => <BootSplash />)

    const rows = container.querySelectorAll('.boot-splash-progress-row')
    expect(rows).toHaveLength(BOOT_SPLASH_PHASES.length)
    for (const [index, phase] of BOOT_SPLASH_PHASES.entries()) {
      expect(rows[index]).toHaveClass(`boot-splash-row-${phase.key}`)
      expect(rows[index]!.querySelector('.boot-splash-progress-label')).toHaveTextContent(phase.label)
    }
  })

  it('gives every row a Lucide Check and three dots in a shared status slot', () => {
    const { container } = render(() => <BootSplashProgress />)

    for (const row of container.querySelectorAll('.boot-splash-progress-row')) {
      const status = row.querySelector('.boot-splash-progress-status')!
      expect(status.querySelector('.boot-splash-progress-check polyline')).toHaveAttribute('points', '20 6 9 17 4 12')
      expect(status.querySelectorAll('.boot-splash-progress-dot')).toHaveLength(3)
    }
  })
})
