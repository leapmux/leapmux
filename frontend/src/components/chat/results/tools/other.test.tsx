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

describe('other renderer', () => {
  checkKindModule({
    kind: 'other',
    request: { args: { q: 1 } },
    titlePart: 'RAW TITLE',
    result: { content: [{ type: 'text', text: 'generic answer' }] },
    resultPart: 'generic answer',
  })
})
