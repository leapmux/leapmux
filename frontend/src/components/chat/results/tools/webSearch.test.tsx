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

describe('web_search renderer', () => {
  checkKindModule({
    kind: 'web_search',
    request: { query: 'solidjs docs', queries: ['solidjs docs', 'solidjs guide'] },
    minimalTitlePart: 'q',
    titlePart: 'solidjs docs',
    result: { links: [{ title: 'Docs', url: 'https://solidjs.com' }], summary: 'found' },
    resultPart: 'Docs',
  })
})
