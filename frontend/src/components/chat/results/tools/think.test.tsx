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

describe('think renderer', () => {
  checkKindModule({
    kind: 'think',
    request: { text: 'consider the options' },
    titlePart: 'RAW TITLE',
    result: { text: 'a thought', format: 'plain' },
    resultPart: 'a thought',
  })
})
