import type { ToolResults } from '~/components/chat/ir/tools'
import { beforeAll, describe, expect, it } from 'vitest'
import { checkKindModule } from '~/test-support/kindTestHarness'
import { toolCallIr, toolRow } from '~/test-support/toolCallIr'
import { toolCallMeta } from './meta'

// jsdom does not provide ResizeObserver, which the shared layouts observe with.
beforeAll(() => {
  globalThis.ResizeObserver ??= class {
    observe() {}
    unobserve() {}
    disconnect() {}
  } as unknown as typeof ResizeObserver
})

const LONG_TEXT = Array.from({ length: 40 }, (_, index) => `line ${index}`).join('\n')

describe('read renderer', () => {
  checkKindModule({
    kind: 'read',
    request: { path: '/p/a.ts', offset: 5, limit: 10 },
    minimalTitlePart: 'a.ts',
    titlePart: 'a.ts',
    result: { lines: [{ num: 5, text: 'alpha' }], fallbackContent: '' },
    resultPart: 'alpha',
  })

  /** Whether the toolbar offers Expand over one read result. */
  const collapsibleOf = (result: ToolResults['read']): boolean =>
    toolCallMeta(toolRow(toolCallIr('read', { result }))).collapsible

  // `[]` is TRUTHY, so the old test measured a list of zero lines and never reached
  // the fallback -- where a refused read states the reason that is the whole answer.
  it('reads an empty line list as no lines at all', () => {
    expect(collapsibleOf({ lines: [], fallbackContent: LONG_TEXT })).toBe(true)
    expect(collapsibleOf({ lines: [], fallbackContent: 'refused' })).toBe(false)
  })

  /**
   * A reminder alone earns the Expand control.
   *
   * `ReadFileResultBody` draws both alert lists only while the row is expanded. A
   * partial-read notice above one short body line therefore had no route to the
   * screen at all: the row answered `collapsible: false` and drew no chevron.
   */
  it('offers Expand for a reminder that sits above a short body', () => {
    const short = { lines: [{ num: 1, text: 'alpha' }], fallbackContent: '' }
    expect(collapsibleOf(short)).toBe(false)
    expect(collapsibleOf({ ...short, leading: [{ label: 'System Reminder', text: 'PARTIAL view' }] })).toBe(true)
    expect(collapsibleOf({ ...short, trailing: [{ label: 'System Reminder', text: 'usage' }] })).toBe(true)
  })

  it('still offers Expand for a body longer than the collapsed rows', () => {
    const lines = Array.from({ length: 40 }, (_, index) => ({ num: index + 1, text: `line ${index}` }))
    expect(collapsibleOf({ lines, fallbackContent: '' })).toBe(true)
  })
})
