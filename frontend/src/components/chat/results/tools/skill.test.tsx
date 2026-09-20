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

describe('skill renderer', () => {
  checkKindModule({
    kind: 'skill',
    request: { name: 'deploy' },
    titlePart: 'deploy',
    result: { text: 'ran the skill', format: 'plain' },
    resultPart: 'ran the skill',
  })
})
