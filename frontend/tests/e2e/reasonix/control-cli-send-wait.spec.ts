import { expect } from '@playwright/test'
import { withCleanup } from '../helpers/cleanup'
import { mintCLITokenForAdmin, runCLI } from '../helpers/cli'
import { currentNativeAgent, nativeTextStep } from '../helpers/nativeScenario'
import { bashToolCall } from '../helpers/providerToolCalls'
import { expectSteerAccepted, queuedInputRow, steerButton } from '../helpers/steer'
import { assistantBubbles, chooseSettingsOption, userBubbles, waitForAgentIdle, waitForControlBanner, waitForSettingsIdle } from '../helpers/ui'
import { reasonixTest } from '../reasonix-fixtures'
import { bypassToolRequests } from './scenarios'

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

reasonixTest('retries the exact reply while a later native permission is open', async ({ native }) => {
  const { page, modelScript, leapmuxServer } = native
  await chooseSettingsOption(page, 'tool_approval-ask')
  await waitForSettingsIdle(page)
  const { id } = await currentNativeAgent(native)
  const cli = await mintCLITokenForAdmin(leapmuxServer)
  const prompt = 'Report the Reasonix release checklist status.'
  const answer = 'The Reasonix release checklist passed.'
  await modelScript.queue(nativeTextStep(native, answer))
  const first = keyedSend(id, modelScript.prompt(prompt), 'reasonix-release-checklist').concat(waitFlags)
  const completed = await runCLI(cli, first) as { input_id: string }
  expect(completed).toMatchObject({
    input_id: expect.any(String),
    turn_outcome: 'completed',
    replies: [answer],
  })
  await waitForAgentIdle(page)

  const nextReply = 'The approved Reasonix shell check completed.'
  await modelScript.queue({
    toolCalls: [
      bashToolCall(native.provider, 'reasonix-cli-permission', 'printf reasonix-permission-proof > reasonix-cli-permission-probe.txt'),
    ],
  }, nativeTextStep(native, nextReply))
  const second = keyedSend(id, modelScript.prompt('Run the scripted Reasonix shell check.'), 'reasonix-release-permission').concat(waitFlags)
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

reasonixTest('returns only the post-steer reply for a mid-turn root steer', async ({ native }) => {
  const { page, modelScript, leapmuxServer } = native
  await bypassToolRequests(native)
  const { id: agentId } = await currentNativeAgent(native)
  const cli = await mintCLITokenForAdmin(leapmuxServer)
  const gate = 'reasonix-cli-steer-exact-reply'
  const beforeReply = 'Running the initial Reasonix release check.'
  const afterReply = 'Included the changelog summary after the Reasonix steer.'
  const guidance = 'Also include the Reasonix changelog summary.'
  const lead = keyedSend(agentId, modelScript.prompt('Run the Reasonix release check.'), 'reasonix-steer-lead')
  const steer = keyedSend(agentId, modelScript.prompt(guidance), 'reasonix-steer-followup')

  await withCleanup(async () => {
    const start = await modelScript.queue(
      {
        gate,
        text: beforeReply,
        toolCalls: [
          bashToolCall(native.provider, 'reasonix-cli-steer-shell', 'printf reasonix-steer-ready'),
        ],
      },
      nativeTextStep(native, afterReply),
    )
    const { input_id: leadId } = await runCLI(cli, lead) as { input_id: string }
    await modelScript.waitForGate(gate)

    const { input_id: steerId } = await runCLI(cli, steer) as { input_id: string }
    expect(steerId).not.toBe(leadId)
    const row = queuedInputRow(page, guidance)
    await steerButton(row).click()
    await expectSteerAccepted(page, row)
    await modelScript.releaseGate(gate)

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
    await modelScript.releaseGateIfHeld(gate)
  })
})
