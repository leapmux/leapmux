import { expect } from '@playwright/test'
import { diracTest } from '../dirac-fixtures'
import { withCleanup } from '../helpers/cleanup'
import { mintCLITokenForAdmin, runCLI } from '../helpers/cli'
import { currentNativeAgent, nativeTextStep } from '../helpers/nativeScenario'
import { bashToolCall } from '../helpers/providerToolCalls'
import { expectSteerAccepted, queuedInputRow, steerButton } from '../helpers/steer'
import { assistantBubbles, userBubbles, waitForAgentIdle, waitForControlBanner, waitForSettingsHydrated } from '../helpers/ui'
import { nativeContext } from './scenarios'

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

diracTest('retries the exact reply while a later native permission is open', async ({ askingDiracWorkspace, page, leapmuxServer, modelScript }) => {
  const context = await nativeContext({
    page,
    modelScript,
    leapmuxServer,
    workspaceId: askingDiracWorkspace.workspaceId,
  })
  await waitForSettingsHydrated(page)
  const { id } = await currentNativeAgent(context)
  const cli = await mintCLITokenForAdmin(leapmuxServer)
  const prompt = 'Report the Dirac release checklist status.'
  const answer = 'The Dirac release checklist passed.'
  await modelScript.queue(nativeTextStep(context, answer))
  const first = keyedSend(id, modelScript.prompt(prompt), 'dirac-release-checklist').concat(waitFlags)
  const completed = await runCLI(cli, first) as { input_id: string }
  expect(completed).toMatchObject({
    input_id: expect.any(String),
    turn_outcome: 'completed',
    replies: [answer],
  })
  await waitForAgentIdle(page)

  const nextReply = 'The approved Dirac shell check completed.'
  await modelScript.queue({
    toolCalls: [
      bashToolCall(context.provider, 'dirac-cli-permission', 'printf dirac-permission-proof'),
    ],
  }, nativeTextStep(context, nextReply))
  const second = keyedSend(id, modelScript.prompt('Run the scripted Dirac shell check.'), 'dirac-release-permission').concat(waitFlags)
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
  await expect(assistantBubbles(page).filter({ hasText: nextReply }).first()).toBeVisible()
})

diracTest('returns only the post-steer reply for a mid-turn root whisper', async ({ native }) => {
  const { page, modelScript, leapmuxServer } = native
  const { id: agentId } = await currentNativeAgent(native)
  const cli = await mintCLITokenForAdmin(leapmuxServer)
  const gate = 'dirac-cli-steer-exact-reply'
  const beforeReply = 'Running the initial Dirac release check.'
  const afterReply = 'Included the changelog summary after the Dirac whisper.'
  const guidance = 'Also include the Dirac changelog summary.'
  const lead = keyedSend(agentId, modelScript.prompt('Run the Dirac release check.'), 'dirac-steer-lead')
  const steer = keyedSend(agentId, modelScript.prompt(guidance), 'dirac-steer-followup')

  await withCleanup(async () => {
    const start = await modelScript.queue(
      {
        gate,
        text: beforeReply,
        toolCalls: [
          bashToolCall(native.provider, 'dirac-cli-steer-shell', 'printf dirac-steer-ready'),
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

    const leadResult = await runCLI(cli, lead.concat(waitFlags)) as { input_id: string, turn_outcome: string, replies: string[] }
    expect(leadResult).toMatchObject({
      input_id: leadId,
      turn_outcome: 'completed',
    })
    expect(leadResult.replies.join(' ')).toContain(beforeReply)
    expect(leadResult.replies.at(-1)).toBe(afterReply)

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
