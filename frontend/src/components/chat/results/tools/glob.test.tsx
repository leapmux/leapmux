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

describe('glob renderer', () => {
  checkKindModule({
    kind: 'glob',
    request: { pattern: '*.ts', paths: [] },
    minimalTitlePart: '*',
    titlePart: '*.ts',
    result: { filenames: [], content: '', numFiles: 0, numLines: 0, truncated: false, fallbackContent: '', empty: false },
  })
})
