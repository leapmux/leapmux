import { expect } from '@playwright/test'
import { gooseTest } from '../goose-fixtures'
import { LIVE_CHILD_SPAWN_CALL_ID } from '../helpers/liveChildTranscript'
import { selectedAgentTabId } from '../helpers/nativeScenario'
import { gooseFinalOutputToolCall, goosePermissionJudgmentToolCall, spawnSubagentToolCall } from '../helpers/providerToolCalls'
import { expectRowBecomesFinal, openChildTabFromRow, requireRegistryRow } from '../helpers/subagentRegistry'
import { answerControl, messageContents, sendMessage, subagentReportBubble, tabById, waitForControlBanner } from '../helpers/ui'

gooseTest('shows the child transcript while the child still runs', async ({ native }) => {
  const { page, modelScript, provider } = native
  const parentId = await selectedAgentTabId(page)
  // Goose 1.55 no longer forwards a subagent's tool requests: the
  // `subagent_tool_request` logging notification has no emitter in its source,
  // so a live child tab carries its prompt and its row, not tool-request rows.
  // Smart Approve still holds each child tool for a reader approval, and the
  // judge clears only the parent's delegate: the child's shell call and its
  // final-output call each raise a control card that this test answers. The
  // queue states the whole round in order: the parent's delegate, the child's
  // shell probe, the child's gated final answer, and the parent's closing text.
  await modelScript.rule({
    name: 'the permission judge clears the delegate',
    when: { system: 'permission-safety classifier' },
    respond: {
      toolCalls: [
        goosePermissionJudgmentToolCall('judge-goose-live', [LIVE_CHILD_SPAWN_CALL_ID]),
      ],
    },
  })
  const gate = 'goose-live-child-held'
  const start = await modelScript.queue(
    {
      toolCalls: [
        spawnSubagentToolCall(provider, LIVE_CHILD_SPAWN_CALL_ID, {
          description: 'Run the live shell probe',
          prompt: modelScript.prompt('Run `echo goose-live` and report the result.'),
        }),
      ],
    },
    {
      toolCalls: [
        {
          id: 'goose-live-shell',
          name: 'shell',
          arguments: { command: 'echo goose-live' },
        },
      ],
    },
    {
      toolCalls: [
        gooseFinalOutputToolCall('live-child-final-goose', 'GOOSE_CHILD_LIVE_DONE'),
      ],
      gate,
    },
    { text: 'The child reported the probe output.' },
  )
  await sendMessage(page, modelScript.prompt('Delegate the live shell probe to a child.'))
  // The shell card sits on the parent conversation; the child cannot reach its
  // gated final answer until a reader allows the shell call.
  await waitForControlBanner(page)
  await answerControl(page, 'allow')
  await modelScript.waitForGate(gate)

  // The row opens while the child still runs, and its child tab holds the
  // child's own prompt.
  const row = await requireRegistryRow(page)
  await expect(row).toHaveAttribute('data-status', 'running')
  await openChildTabFromRow(page, row)
  await expect(messageContents(page).filter({ hasText: 'echo goose-live' }).first()).toBeVisible()

  // The released final answer ends the child through its final-output tool,
  // whose approval this test answers back on the parent conversation.
  await modelScript.releaseGate(gate)
  await tabById(page, parentId).click()
  await waitForControlBanner(page)
  await answerControl(page, 'allow')
  await modelScript.waitForSteps(start + 4)
  await expectRowBecomesFinal(page, row)

  // The finished child reports through its exported final output.
  await openChildTabFromRow(page, row)
  await expect(subagentReportBubble(page, /GOOSE_CHILD_LIVE_DONE/)).toBeVisible()
})
