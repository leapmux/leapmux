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

describe('wait renderer', () => {
  checkKindModule({
    kind: 'wait',
    request: { durationMs: 30000 },
    titlePart: '30s',
    result: { text: 'waited', format: 'plain' },
    resultPart: 'waited',
  })
})
