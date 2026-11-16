import type { ToolCallFixtureOptions } from '~/test-support/toolCallFixture'
import { render } from '@solidjs/testing-library'
import { beforeAll, describe, expect, it } from 'vitest'
import { settleWorkerReplies } from '~/test-support/fakeWorker'
import { checkKindModule } from '~/test-support/kindTestHarness'
import { toolCallFixture, toolRow } from '~/test-support/toolCallFixture'
import { failedResult, unparsedResult } from '../../model/toolCall'
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

  // Once the call ends, the request row never reads how it ended, and the pending row
  // leaves. A call that ended with NO result -- the turn stopped it -- leaves the proposal
  // as the one thing to read, on the row that ended it. A result of any form, in words
  // or typed, answers for the call instead: a refused plan must not read as one that
  // stands.
  describe('a proposal whose call ended', () => {
    const ended = (options: Pick<ToolCallFixtureOptions<'report'>, 'result' | 'status'>) => toolRow(
      toolCallFixture('report', {
        title: 'Add CHANGELOG.md',
        request: { proposal: '# Add CHANGELOG.md\n\nPLANMARKER-8\n' },
        ...options,
      }),
      'result',
      { request: true },
    )

    it('draws the proposal on the row that ended a call with no result', async () => {
      const { container } = render(() => <ToolMessage row={ended({ status: 'incomplete' })} />)
      await settleWorkerReplies()
      expect(container.textContent).toContain('PLANMARKER-8')
    })

    it('draws the words alone when the result states words of its own', async () => {
      const { container } = render(() => (
        <ToolMessage row={ended({
          result: unparsedResult('Plan approved'),
        })}
        />
      ))
      await settleWorkerReplies()
      expect(container.textContent).toContain('Plan approved')
      expect(container.textContent).not.toContain('PLANMARKER-8')
    })

    it('states only the reason of a declined proposal', async () => {
      const { container } = render(() => (
        <ToolMessage row={ended({
          status: 'declined',
          result: failedResult('The reader declined the plan.'),
        })}
        />
      ))
      await settleWorkerReplies()
      expect(container.textContent).toContain('The reader declined the plan.')
      expect(container.textContent).not.toContain('PLANMARKER-8')
    })

    it('draws the answer alone when the result states it typed', async () => {
      const { container } = render(() => (
        <ToolMessage row={ended({
          result: { text: 'The plan landed.', format: 'markdown' },
        })}
        />
      ))
      await settleWorkerReplies()
      expect(container.textContent).toContain('The plan landed.')
      expect(container.textContent).not.toContain('PLANMARKER-8')
    })
  })
})
