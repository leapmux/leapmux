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

describe('edit renderer', () => {
  checkKindModule({
    kind: 'edit',
    request: { changes: [{ filePath: '/p/a.ts', oldStr: 'x', newStr: 'y', structuredPatch: null }] },
    titlePart: 'a.ts',
    result: { changes: [{ filePath: '/p/a.ts', oldStr: 'x', newStr: 'y', structuredPatch: null }] },
  })
})
