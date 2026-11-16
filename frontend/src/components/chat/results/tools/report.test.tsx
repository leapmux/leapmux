import type { ToolCallFixtureOptions } from '~/test-support/toolCallFixture'
import { render } from '@solidjs/testing-library'
import { beforeAll, describe, expect, it } from 'vitest'
import { settleWorkerReplies } from '~/test-support/fakeWorker'
import { checkKindModule } from '~/test-support/kindTestHarness'
import { toolCallFixture, toolRow } from '~/test-support/toolCallFixture'
import { unparsedResult } from '../../model/toolCall'
import { ToolMessage } from '../ToolMessage'
import { ToolPendingMessage } from '../ToolPendingMessage'

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
  // approval the call waited on. The PENDING row states it while the approval is open,
  // and the result row draws the answer once the call completes. The request row draws
  // the same before and after, so it never states the proposal.
  //
  // `request.proposal`, the TYPED field: layer 3 knows no provider and parses no
  // provider's bytes, so the plan cannot ride here in the untyped payload bag.
  describe('a proposal that waits for its answer', () => {
    const running = () => toolRow(
      toolCallFixture('report', {
        title: 'Add CHANGELOG.md',
        request: { proposal: '# Add CHANGELOG.md\n\nPLANMARKER-7\n' },
        status: 'in_progress',
      }),
      'request',
    )

    it('draws the plan on the pending row while the approval is open', async () => {
      const { container } = render(() => <ToolPendingMessage row={running()} />)
      await settleWorkerReplies()
      expect(container.textContent).toContain('PLANMARKER-7')
    })

    it('never draws the plan on the request row', () => {
      const { container } = render(() => <ToolMessage row={running()} />)
      expect(container.textContent).not.toContain('PLANMARKER-7')
    })
  })

  // Once the call completes, the result row is the one row that can state the proposal:
  // the request row never reads how the call ended, and the pending row leaves.
  describe('a proposal whose call completed', () => {
    const completed = (result: ToolCallFixtureOptions<'report'>['result']) => toolRow(
      toolCallFixture('report', {
        title: 'Add CHANGELOG.md',
        request: { proposal: '# Add CHANGELOG.md\n\nPLANMARKER-8\n' },
        result,
      }),
      'result',
      { request: true },
    )

    it('draws the proposal on a result row whose result states no words of its own', async () => {
      const { container } = render(() => <ToolMessage row={completed(unparsedResult('Plan approved'))} />)
      await settleWorkerReplies()
      expect(container.textContent).toContain('PLANMARKER-8')
      expect(container.textContent).toContain('Plan approved')
    })

    it('draws the answer alone when the result states words of its own', async () => {
      const { container } = render(() => <ToolMessage row={completed({ text: 'The plan landed.', format: 'markdown' })} />)
      await settleWorkerReplies()
      expect(container.textContent).toContain('The plan landed.')
      expect(container.textContent).not.toContain('PLANMARKER-8')
    })
  })
})
