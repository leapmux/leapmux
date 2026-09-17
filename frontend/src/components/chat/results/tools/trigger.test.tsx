import type { ToolCallIR } from '../../ir/toolCall'
import type { ToolRowView } from './renderer'
import { render } from '@solidjs/testing-library'
import { beforeAll, describe, expect, it } from 'vitest'
import { ToolMessage } from '~/components/chat/results/ToolMessage'
import { checkKindModule } from '~/test-support/kindTestHarness'
import { toolCallIr, toolRow } from '~/test-support/toolCallIr'
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
  it.each([[undefined], [{ text: '', format: 'plain' as const }]])('falls back to the label for an unworded action (result: %s)', (result) => {
    const call = toolCallIr('trigger', { request: { action: 'other' }, ...(result ? { result } : {}) })
    expect(triggerRenderer.title(parsedCall(call), undefined)).toBe('Trigger')
  })

  it('keeps the call\'s own title ahead of the label', () => {
    const call = toolCallIr('trigger', { request: { action: 'other' }, title: 'Nightly build' })
    expect(triggerRenderer.title(parsedCall(call), undefined)).toBe('Nightly build')
  })

  // The SCHEDULE is the one fact a cron entry exists for, and no title states it.
  // Every provider filled it and no row drew it.
  describe('the schedule line', () => {
    const rowText = (call: ToolCallIR) => render(() => <ToolMessage row={toolRow(call)} />).container.textContent ?? ''

    it('draws the schedule while the call runs', () => {
      const call = toolCallIr('trigger', { status: 'in_progress', request: { action: 'create', name: 'nightly', schedule: '0 3 * * *' } })
      expect(rowText(call)).toContain('0 3 * * *')
    })

    // Once the answer lands the endpoint restates the schedule in its own words, so a
    // second copy above it would draw the same fact twice.
    it('draws no schedule once the answer lands', () => {
      const call = toolCallIr('trigger', {
        request: { action: 'create', name: 'nightly', schedule: '0 3 * * *' },
        result: { text: 'created', format: 'plain' },
      })
      expect(rowText(call)).not.toContain('0 3 * * *')
      expect(triggerRenderer.request?.(parsedCall(call), rowView())).toBeNull()
    })

    // A call that states no schedule draws no line at all, rather than an empty one.
    it('draws no line for a call that states no schedule', () => {
      const call = toolCallIr('trigger', { status: 'in_progress', request: { action: 'list' } })
      expect(triggerRenderer.request?.(parsedCall(call), rowView())).toBeNull()
    })
  })
})

/** The view one mounted row hands a drawing hook. The schedule line reads none of it. */
function rowView(): ToolRowView {
  return {
    role: 'result',
    hasRequestRow: false,
    context: undefined,
    drawsResult: true,
    expanded: () => false,
    setExpanded: () => {},
    imageIndexOffset: 0,
    onSummaryOverflow: () => {},
  }
}
