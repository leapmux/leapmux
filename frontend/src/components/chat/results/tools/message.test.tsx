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

describe('message renderer', () => {
  checkKindModule({
    kind: 'message',
    request: { to: 'peer-1', text: 'hello there', summary: 'greeting' },
    titlePart: 'peer-1',
    result: { text: 'sent', format: 'plain' },
    resultPart: 'sent',
  })
})
