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

describe('move renderer', () => {
  checkKindModule({
    kind: 'move',
    request: { changes: [{ filePath: '/p/b.ts', oldStr: '', newStr: '', structuredPatch: null, previousPath: '/p/a.ts', operation: 'move' as const }] },
    titlePart: 'a.ts',
    result: { changes: [{ filePath: '/p/b.ts', oldStr: '', newStr: '', structuredPatch: null, previousPath: '/p/a.ts', operation: 'move' as const }] },
  })
})
