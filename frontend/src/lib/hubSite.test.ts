import { describe, expect, it, vi } from 'vitest'

import { ceremoniesRunInBrowser, hubOrigin, hubPageUrl, openHubPage } from '~/lib/hubSite'

const desktopHubMock = vi.hoisted(() => vi.fn<() => { kind: 'solo' } | { kind: 'remote', url: string } | null>())

vi.mock('~/api/platformBridge', () => ({
  desktopHub: desktopHubMock,
  openExternalUrl: vi.fn(),
}))

describe('hubSite', () => {
  describe('ceremoniesRunInBrowser', () => {
    it('returns true when the desktop hub is remote', () => {
      desktopHubMock.mockReturnValue({ kind: 'remote', url: 'https://hub.example.com' })
      expect(ceremoniesRunInBrowser()).toBe(true)
    })

    it('returns false in a browser', () => {
      desktopHubMock.mockReturnValue(null)
      expect(ceremoniesRunInBrowser()).toBe(false)
    })

    it('returns false in solo mode', () => {
      desktopHubMock.mockReturnValue({ kind: 'solo' })
      expect(ceremoniesRunInBrowser()).toBe(false)
    })
  })

  describe('hubOrigin', () => {
    it('returns the remote hub origin in remote mode', () => {
      desktopHubMock.mockReturnValue({ kind: 'remote', url: 'https://hub.example.com/api' })
      expect(hubOrigin()).toBe('https://hub.example.com')
    })

    it('returns the page origin in a browser', () => {
      desktopHubMock.mockReturnValue(null)
      expect(hubOrigin()).toBe(window.location.origin)
    })

    it('returns the page origin in solo mode', () => {
      desktopHubMock.mockReturnValue({ kind: 'solo' })
      expect(hubOrigin()).toBe(window.location.origin)
    })

    it('normalizes a hub URL in another form', () => {
      desktopHubMock.mockReturnValue({ kind: 'remote', url: 'https://Hub.Example:443/' })
      expect(hubOrigin()).toBe('https://hub.example')
    })
  })

  describe('hubPageUrl', () => {
    it('resolves a path against the hub origin in remote mode', () => {
      desktopHubMock.mockReturnValue({ kind: 'remote', url: 'https://hub.example.com' })
      expect(hubPageUrl('/verify-email')).toBe('https://hub.example.com/verify-email')
    })

    it('resolves a path against the page origin outside remote mode', () => {
      desktopHubMock.mockReturnValue(null)
      expect(hubPageUrl('/verify-email')).toBe(`${window.location.origin}/verify-email`)
    })

    it('throws for a protocol-relative path', () => {
      desktopHubMock.mockReturnValue({ kind: 'remote', url: 'https://hub.example.com' })
      expect(() => hubPageUrl('//evil')).toThrow('unsafe Hub page path: //evil')
    })

    it('throws for a path with a control byte', () => {
      desktopHubMock.mockReturnValue({ kind: 'remote', url: 'https://hub.example.com' })
      expect(() => hubPageUrl('/\t/evil')).toThrow('unsafe Hub page path: /\t/evil')
    })
  })

  describe('openHubPage', () => {
    it('opens the hub page URL in the system browser', async () => {
      const { openExternalUrl } = await import('~/api/platformBridge')
      desktopHubMock.mockReturnValue({ kind: 'remote', url: 'https://hub.example.com' })
      await openHubPage('/verify-email')
      expect(openExternalUrl).toHaveBeenCalledWith('https://hub.example.com/verify-email')
    })
  })
})
