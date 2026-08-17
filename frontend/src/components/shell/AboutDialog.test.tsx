import { cleanup, render, screen, within } from '@solidjs/testing-library'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { AboutDialog } from './AboutDialog'

vi.mock('~/lib/systemInfo', () => ({
  getBackendBuildInfo: () => ({
    version: '1.0.0',
    commitHash: 'abc1234',
    commitTime: '2026-01-15T00:00:00Z',
    buildTime: '2026-01-15T00:00:00Z',
    branch: 'main',
  }),
  getFrontendBuildInfo: () => ({
    version: '1.0.0',
    commitHash: 'abc1234',
    commitTime: '2026-01-15T00:00:00Z',
    buildTime: '2026-01-15T00:00:00Z',
    branch: 'main',
  }),
  formatVersionLine: () => '1.0.0 (abc1234)',
}))

vi.mock('~/api/platformBridge', () => ({
  isTauriApp: () => false,
}))

afterEach(() => {
  cleanup()
})

describe('aboutDialog', () => {
  it('lists leapmux.dev immediately before the GitHub URL under Homepage', () => {
    render(() => <AboutDialog onClose={() => {}} />)

    const homepage = screen.getByText('Homepage').parentElement!
    const links = within(homepage).getAllByRole('link')
    expect(links.map(a => a.getAttribute('href'))).toEqual([
      'https://leapmux.dev/',
      'https://github.com/leapmux/leapmux',
    ])
    expect(links[0]).toHaveTextContent('leapmux.dev')
    expect(links[1]).toHaveTextContent('github.com/leapmux/leapmux')
  })
})
