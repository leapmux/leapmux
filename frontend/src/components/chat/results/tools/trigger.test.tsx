import type { ToolCall } from '../../model/toolCall'
import type { ToolRowView } from './renderer'
import { render } from '@solidjs/testing-library'
import { beforeAll, describe, expect, it } from 'vitest'
import { failedResult } from '~/components/chat/model/toolCall'
import { ToolMessage } from '~/components/chat/results/ToolMessage'
import { ToolPendingMessage } from '~/components/chat/results/ToolPendingMessage'
import { checkKindModule } from '~/test-support/kindTestHarness'
import { toolCallFixture, toolRow } from '~/test-support/toolCallFixture'
import { parsedCall } from './renderer'
import { triggerRenderer } from './trigger'

// jsdom does not provide ResizeObserver, which the shared layouts observe with.
beforeAll(() => {
  globalThis.ResizeObserver ??= class {
    observe() {}
    unobserve() {}
    disconnect() {}
  } as unknown as typeof ResizeObserver
})

describe('trigger renderer', () => {
  checkKindModule({
    kind: 'trigger',
    request: { action: 'create', name: 'nightly' },
    titlePart: 'nightly',
    result: { text: 'created', format: 'plain' },
    resultPart: 'created',
  })

  // `action: 'other'` matches no case of the title switch, which composes ''. A call
  // that also carried no title of its own then reached the header with no words at
  // all, and the row drew a bare icon. Every other kind ends in its own label.
  it.each([
    [undefined],
    [
      { text: '', format: 'plain' as const },
    ],
  ])('falls back to the label for an unworded action (result: %s)', (result) => {
    const call = toolCallFixture('trigger', {
      request: { action: 'other' },
      ...(result
        ? { result }
        : {}),
    })
    expect(triggerRenderer.title(parsedCall(call), undefined)).toBe('Trigger')
  })

  it('keeps the call\'s own title ahead of the label', () => {
    const call = toolCallFixture('trigger', {
      request: { action: 'other' },
      title: 'Nightly build',
    })
    expect(triggerRenderer.title(parsedCall(call), undefined)).toBe('Nightly build')
  })

  // The SCHEDULE is the one fact a cron entry exists for, and no title states it.
  // Every provider filled it and no row drew it.
  describe('the schedule line', () => {
    const pendingText = (call: ToolCall) => render(() => <ToolPendingMessage row={toolRow(call, 'request')} />).container.textContent ?? ''
    const requestText = (call: ToolCall) => render(() => <ToolMessage row={toolRow(call, 'request')} />).container.textContent ?? ''

    it('draws the schedule on the pending row while the call runs', () => {
      const call = toolCallFixture('trigger', {
        status: 'in_progress',
        request: { action: 'create', name: 'nightly', schedule: '0 3 * * *' },
      })
      expect(pendingText(call)).toContain('0 3 * * *')
      expect(triggerRenderer.hasPending?.(parsedCall(call), false)).toBe(true)
    })

    // Once the answer lands the endpoint restates the schedule in its own words, on the
    // result row. The request row draws the same before and after, so it never states
    // the schedule: a second copy would draw the same fact twice.
    it('never draws the schedule on the request row', () => {
      const call = toolCallFixture('trigger', {
        status: 'in_progress',
        request: { action: 'create', name: 'nightly', schedule: '0 3 * * *' },
      })
      expect(requestText(call)).not.toContain('0 3 * * *')
      expect(triggerRenderer.request).toBeUndefined()
    })

    // A call that states no schedule draws no line at all, rather than an empty one, so
    // its pending row draws nothing.
    it('draws no line for a call that states no schedule', () => {
      const call = toolCallFixture('trigger', {
        status: 'in_progress',
        request: { action: 'list' },
      })
      expect(triggerRenderer.pending?.(parsedCall(call), rowView())).toBeNull()
      expect(triggerRenderer.hasPending?.(parsedCall(call), false)).toBe(false)
    })

    // The pending row leaves when the result row joins the list. A call that ended with no
    // answer states its schedule on the row that ended it; an answer in words restates it.
    const resultText = (call: ToolCall) => render(() => <ToolMessage row={toolRow(call, 'result', { request: true })} />).container.textContent ?? ''

    it('draws the schedule on the row of a call that ended with no answer', () => {
      const call = toolCallFixture('trigger', {
        status: 'cancelled',
        request: { action: 'create', name: 'nightly', schedule: '0 3 * * *' },
      })
      expect(resultText(call)).toContain('0 3 * * *')
    })

    it('draws no schedule beside an answer in words', () => {
      const call = toolCallFixture('trigger', {
        status: 'failed',
        request: { action: 'create', name: 'nightly', schedule: '0 3 * * *' },
        result: failedResult('The scheduler refused the job.'),
      })
      const text = resultText(call)
      expect(text).toContain('The scheduler refused the job.')
      expect(text).not.toContain('0 3 * * *')
    })
  })
})

/** The view one mounted row hands a drawing hook. The schedule line reads none of it. */
function rowView(): ToolRowView {
  return {
    role: 'request',
    context: undefined,
    expanded: () => false,
    setExpanded: () => {},
    imageIndexOffset: 0,
    onSummaryOverflow: () => {},
  }
}
