import { expect } from '@playwright/test'
import { gooseTest } from '../goose-fixtures'
import { withCleanup } from '../helpers/cleanup'
import { mintCLITokenForAdmin, runCLI } from '../helpers/cli'
import { currentNativeAgent, nativeTextStep } from '../helpers/nativeScenario'
import { bashToolCall } from '../helpers/providerToolCalls'
import { expectSteerAccepted, queuedInputRow, steerButton } from '../helpers/steer'
import { createToolOutputControl } from '../helpers/toolOutputControl'
import { assistantBubbles, userBubbles, waitForAgentIdle, waitForControlBanner } from '../helpers/ui'
import { askBeforeEachTool, bypassToolRequests } from './scenarios'

function keyedSend(agentId: string, prompt: string, requestId: string): string[] {
  return [
    'agent',
    'send',
    agentId,
    prompt,
    '--request-id',
    requestId,
  ]
}

const waitFlags = [
  '--wait',
  '--all',
  '--timeout',
  '25s',
]

gooseTest('retries the exact reply while a later native permission is open', async ({ native }) => {
  const { page, modelScript, leapmuxServer } = native
  await askBeforeEachTool(native)
  const { id } = await currentNativeAgent(native)
  const cli = await mintCLITokenForAdmin(leapmuxServer)
  const prompt = 'Report the Goose release checklist status.'
  const answer = 'The Goose release checklist passed.'
  await modelScript.queue(nativeTextStep(native, answer))
  const first = keyedSend(id, modelScript.prompt(prompt), 'goose-release-checklist').concat(waitFlags)
  const completed = await runCLI(cli, first) as { input_id: string }
  expect(completed).toMatchObject({
    input_id: expect.any(String),
    turn_outcome: 'completed',
    replies: [answer],
  })
  await waitForAgentIdle(page)

  const nextReply = 'The approved Goose shell check completed.'
  await modelScript.queue({
    toolCalls: [
      bashToolCall(native.provider, 'goose-cli-permission', 'printf goose-permission-proof'),
    ],
  }, nativeTextStep(native, nextReply))
  const second = keyedSend(id, modelScript.prompt('Run the scripted Goose shell check.'), 'goose-release-permission').concat(waitFlags)
  const waiting = await runCLI(cli, second) as { input_id: string }
  expect(waiting).toMatchObject({
    input_id: expect.any(String),
    state: 'waiting_for_user',
  })
  await waitForControlBanner(page)
  await expect(page.locator('[data-testid="thinking-indicator"]:visible')).toHaveCount(0)
  expect(await runCLI(cli, first)).toMatchObject({
    input_id: completed.input_id,
    turn_outcome: 'completed',
    replies: [answer],
  })
  const { requests } = await runCLI(cli, ['agent', 'requests', id]) as { requests: Array<{ id: string }> }
  expect(requests).toHaveLength(1)
  await runCLI(cli, [
    'agent',
    'approve',
    id,
    requests[0]!.id,
  ])
  expect(await runCLI(cli, second)).toMatchObject({
    input_id: waiting.input_id,
    turn_outcome: 'completed',
    replies: [nextReply],
  })
  await expect(userBubbles(page).filter({ hasText: prompt })).toHaveCount(1)
  await expect(assistantBubbles(page).filter({ hasText: nextReply })).toHaveCount(1)
})

gooseTest('returns only the post-steer reply for a mid-turn root steer', async ({ native }) => {
  const { page, modelScript, leapmuxServer } = native
  await bypassToolRequests(native)
  const agent = await currentNativeAgent(native)
  const cli = await mintCLITokenForAdmin(leapmuxServer)
  const tool = createToolOutputControl(agent.workingDir)
  const beforeReply = 'Running the initial Goose release check.'
  const afterReply = 'Included the changelog summary after the Goose steer.'
  const guidance = 'Also include the Goose changelog summary.'
  const lead = keyedSend(agent.id, modelScript.prompt('Run the Goose release check.'), 'goose-steer-lead')
  const steer = keyedSend(agent.id, modelScript.prompt(guidance), 'goose-steer-followup')

  await withCleanup(async () => {
    const start = await modelScript.queue(
      {
        text: beforeReply,
        toolCalls: [
          bashToolCall(native.provider, 'goose-cli-steer-shell', tool.command),
        ],
      },
      nativeTextStep(native, afterReply),
    )
    const { input_id: leadId } = await runCLI(cli, lead) as { input_id: string }
    await modelScript.waitForSteps(start + 1)
    await tool.waitForFirstOutput()

    const { input_id: steerId } = await runCLI(cli, steer) as { input_id: string }
    expect(steerId).not.toBe(leadId)
    const row = queuedInputRow(page, guidance)
    await steerButton(row).click()
    await expectSteerAccepted(page, row)

    await tool.releaseFirstOutput()
    await tool.waitForSecondOutput()
    await tool.releaseFinalOutput()

    await modelScript.waitForSteps(start + 2)
    await waitForAgentIdle(page)

    expect(await runCLI(cli, lead.concat(waitFlags))).toMatchObject({
      input_id: leadId,
      turn_outcome: 'completed',
      replies: [beforeReply, afterReply],
    })
    expect(await runCLI(cli, steer.concat(waitFlags))).toMatchObject({
      input_id: steerId,
      turn_outcome: 'completed',
      replies: [afterReply],
    })
    expect(await runCLI(cli, steer.concat(waitFlags))).toMatchObject({
      input_id: steerId,
      turn_outcome: 'completed',
      replies: [afterReply],
    })
  }, async () => {
    await tool.releaseFirstOutput()
    await tool.releaseFinalOutput()
  })
})
