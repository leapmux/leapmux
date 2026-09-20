import { render } from '@solidjs/testing-library'
import { beforeAll, describe, expect, it } from 'vitest'
import { checkKindModule } from '~/test-support/kindTestHarness'
import { toolCallFixture, toolRow } from '~/test-support/toolCallFixture'
import { ToolMessage } from '../ToolMessage'

// jsdom does not provide ResizeObserver, which the shared layouts observe with.
beforeAll(() => {
  globalThis.ResizeObserver ??= class {
    observe() {}
    unobserve() {}
    disconnect() {}
  } as unknown as typeof ResizeObserver
})

describe('report renderer', () => {
  checkKindModule({
    kind: 'report',
    request: {},
    titlePart: 'RAW TITLE',
    result: { text: 'report body', format: 'markdown' },
    resultPart: 'report body',
  })

  // A proposal that has not drawn its answer yet -- Cursor's plan, recovered from the
  // approval the call waited on. The proposing row states it while the approval is
  // open, and the row that completes the call takes the drawing over from there.
  //
  // `request.proposal`, the TYPED field: layer 3 knows no provider and parses no
  // provider's bytes, so the plan cannot ride here in the untyped payload bag.
  describe('a proposal that drew its answer', () => {
    // An OPEN approval is a call that has not answered, and such a call carries no
    // result: the status moves with the body the case states.
    const planCall = (result?: { text: string, format: 'markdown' }, hasResultRow = false) => toolRow(
      toolCallFixture('report', {
        title: 'Add CHANGELOG.md',
        request: { proposal: '# Add CHANGELOG.md\n\nPLANMARKER-7\n' },
        status: result ? 'completed' : 'in_progress',
        result,
      }),
      'request',
      { result: hasResultRow },
    )

    it('draws the plan while the approval is open', () => {
      const { container } = render(() => <ToolMessage row={planCall()} />)
      expect(container.textContent).toContain('PLANMARKER-7')
    })

    it('hands the drawing to the row that completed the call', () => {
      const { container } = render(() => <ToolMessage row={planCall({ text: '# Add CHANGELOG.md\n\nPLANMARKER-7\n', format: 'markdown' }, true)} />)
      expect(container.textContent).not.toContain('PLANMARKER-7')
    })
  })
})
