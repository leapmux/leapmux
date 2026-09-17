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

describe('chart renderer', () => {
  checkKindModule({
    kind: 'chart',
    request: { spec: '{}', title: 'Latency' },
    titlePart: 'Latency',
    result: { shape: 'bar', labels: [], series: [] },
  })
})
