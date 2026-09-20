import { beforeAll, describe } from 'vitest'
import { checkKindModule } from '~/test-support/kindTestHarness'

// jsdom does not provide ResizeObserver, which the shared layouts observe with.
beforeAll(() => {
  globalThis.ResizeObserver ??= class {
    observe() {}
    unobserve() {}
    disconnect() {}
  } as unknown as typeof ResizeObserver
})

describe('fetch renderer', () => {
  checkKindModule({
    kind: 'fetch',
    request: { url: 'https://example.com' },
    minimalTitlePart: 'https://example.com',
    titlePart: 'example.com',
    result: { result: 'page text' },
    resultPart: 'page text',
  })
})
