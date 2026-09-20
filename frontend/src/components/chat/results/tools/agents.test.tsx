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

describe('agents renderer', () => {
  checkKindModule({
    kind: 'agents',
    request: { team: { name: 'Platform' } },
    titlePart: 'Platform',
    result: { text: 'one agent', format: 'plain' },
    resultPart: 'one agent',
  })
})
