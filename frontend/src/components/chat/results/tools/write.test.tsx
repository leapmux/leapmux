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

describe('write renderer', () => {
  checkKindModule({
    kind: 'write',
    request: { changes: [{ filePath: '/p/new.ts', oldStr: '', newStr: 'one\n', structuredPatch: null, operation: 'add' as const }] },
    titlePart: 'new.ts',
    result: { changes: [{ filePath: '/p/new.ts', oldStr: '', newStr: 'one\n', structuredPatch: null, operation: 'add' as const }] },
  })
})
