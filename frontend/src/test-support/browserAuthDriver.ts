import { vi } from 'vitest'

export interface BrowserAuthResultFixture {
  ceremonyId: string
  ok: boolean
  failure: string
}

export const DEFAULT_BROWSER_AUTH_START = {
  ceremonyId: 'c-1',
  authorizeUrl: 'https://hub.example/authorize',
}

type BrowserAuthResultHandler = (payload: BrowserAuthResultFixture) => void

export const browserAuthDriver = {
  openExternalUrl: vi.fn<(url: string) => Promise<void>>(),
  startBrowserAuth: vi.fn<() => Promise<typeof DEFAULT_BROWSER_AUTH_START>>(),
  cancelBrowserAuth: vi.fn<(ceremonyId: string) => Promise<void>>(),
  handlers: [] as BrowserAuthResultHandler[],
  emit(payload: BrowserAuthResultFixture): void {
    for (const handler of [...browserAuthDriver.handlers])
      handler(payload)
  },
}

export function resetBrowserAuthDriver(): void {
  browserAuthDriver.openExternalUrl.mockReset()
  browserAuthDriver.openExternalUrl.mockResolvedValue(undefined)
  browserAuthDriver.startBrowserAuth.mockReset()
  browserAuthDriver.startBrowserAuth.mockResolvedValue(DEFAULT_BROWSER_AUTH_START)
  browserAuthDriver.cancelBrowserAuth.mockReset()
  browserAuthDriver.cancelBrowserAuth.mockResolvedValue(undefined)
  browserAuthDriver.handlers.length = 0
}

export function browserAuthPlatformMock() {
  return {
    openExternalUrl: (url: string) => browserAuthDriver.openExternalUrl(url),
    platformBridge: {
      startBrowserAuth: () => browserAuthDriver.startBrowserAuth(),
      cancelBrowserAuth: (ceremonyId: string) => browserAuthDriver.cancelBrowserAuth(ceremonyId),
      onEvent: (_event: string, handler: BrowserAuthResultHandler): Promise<() => void> => {
        browserAuthDriver.handlers.push(handler)
        return Promise.resolve(vi.fn(() => {
          const index = browserAuthDriver.handlers.indexOf(handler)
          if (index >= 0)
            browserAuthDriver.handlers.splice(index, 1)
        }))
      },
    },
  }
}
