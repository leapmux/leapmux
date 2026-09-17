import { render, screen } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { PI_PLAN_ACTION } from '~/generated/contracts/pi-protocol'
import { createControlAnswerState } from '../../controls/types'
import { PiPlanApprovalActions } from './PiPlanApprovalActions'

/*
 * Pi degrades its plan approval into a `select` walker, and that select carries SIX
 * options. Three are the decisions LeapMux states as Approve and Reject; the rest --
 * Export plan…, Save for later, Discard plan and exit -- belong to Pi alone, and a
 * reader who cannot reach them cannot do what the agent offered.
 *
 * The provider catalog recorded the two named ones as unreachable. They are not: the
 * component routes every option the select carries that is not one of the three
 * decisions into the overflow menu. This pins that, so the claim stays true.
 */
const PLAN_OPTIONS = [
  'Implement here',
  'Start fresh and implement',
  'Export plan…',
  'Save for later',
  'Stay in Plan mode',
  'Discard plan and exit',
]

function renderActions(onRespond = vi.fn(async () => {})) {
  const request = {
    requestId: 'req-1',
    payload: { type: 'extension_ui_request', method: 'select', title: 'Proposed plan ready. What next?', options: PLAN_OPTIONS },
  }
  render(() => (
    <PiPlanApprovalActions
      request={request as never}
      onRespond={onRespond}
      answerState={createControlAnswerState({})}
      hasEditorContent={false}
      onTriggerSend={vi.fn()}
    />
  ))
  return onRespond
}

function sentValue(onRespond: ReturnType<typeof vi.fn>): string {
  const bytes = onRespond.mock.calls[0]?.[0] as Uint8Array
  return JSON.parse(new TextDecoder().decode(bytes)).value
}

describe('pi plan approval options', () => {
  it.each(['Export plan…', 'Save for later', 'Discard plan and exit'])('offers %s', async (label) => {
    const onRespond = renderActions()
    const item = await screen.findByRole('menuitem', { name: label, hidden: true })
    item.click()
    expect(sentValue(onRespond)).toBe(label)
  })

  // The three the shared decision row already states must NOT appear a second time in
  // the overflow menu, or the reader sees Approve twice under different words.
  it.each([PI_PLAN_ACTION.ImplementHere, PI_PLAN_ACTION.ImplementFresh, PI_PLAN_ACTION.Stay])(
    'keeps %s out of the overflow menu',
    (label) => {
      renderActions()
      expect(screen.queryByRole('menuitem', { name: label, hidden: true })).toBeNull()
    },
  )

  it('sends the stay action for a rejection', () => {
    const onRespond = renderActions()
    screen.getByTestId('plan-reject-btn').click()
    expect(sentValue(onRespond)).toBe(PI_PLAN_ACTION.Stay)
  })
})
