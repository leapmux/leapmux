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

describe('task renderer', () => {
  checkKindModule({
    kind: 'task',
    request: { action: 'output', taskId: 't-9' },
    titlePart: 'RAW TITLE',
    result: { title: 'Task output', outcome: 'completed', output: 'the output' },
    resultPart: 'the output',
  })
})
