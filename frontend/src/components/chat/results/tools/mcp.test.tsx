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

describe('mcp renderer', () => {
  checkKindModule({
    kind: 'mcp',
    request: { args: { q: 1 }, server: 'Docs', tool: 'lookup' },
    minimalTitlePart: 's / t',
    titlePart: 'Docs / lookup',
    result: { content: [{ type: 'text', text: 'found it' }] },
    resultPart: 'found it',
  })
})
