import type { CLIConfigDir } from '../helpers/cli'
import { existsSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { codewhaleTest } from '../codewhale-fixtures'
import { withCleanup } from '../helpers/cleanup'
import { mintCLITokenForAdmin, runCLI } from '../helpers/cli'
import { currentNativeAgent, nativeScenarioModelContextText, nativeTextStep } from '../helpers/nativeScenario'
import { bashToolCall } from '../helpers/providerToolCalls'
import { expectSteerAccepted, queuedInputRow, steerButton } from '../helpers/steer'
import { assistantBubbles, userBubbles, waitForAgentIdle, waitForControlBanner } from '../helpers/ui'

function sendAndWait(agentId: string, prompt: string, requestId: string): string[] {
  return [
    'agent',
    'send',
    agentId,
    prompt,
    '--request-id',
    requestId,
    '--wait',
    '--timeout',
    '20s',
  ]
}

async function approveCodewhalePermission(cli: CLIConfigDir, agentId: string): Promise<void> {
  const { requests } = await runCLI(cli, ['agent', 'requests', agentId]) as { requests: Array<{ id: string }> }
  expect(requests).toHaveLength(1)
  const [permission] = requests
  if (!permission)
    throw new Error('The Worker returned no CodeWhale permission request.')
  await runCLI(cli, [
    'agent',
    'approve',
    agentId,
    permission.id,
  ])
}

codewhaleTest('returns only the reply after a steer read by a streaming turn', async ({ native, leapmuxServer }) => {
  const { page, modelScript } = native
  const { id: agentId, workingDir } = await currentNativeAgent(native)
  const cli = await mintCLITokenForAdmin(leapmuxServer)
  const leadReply = 'The original review is ready.'
  const steerPrompt = 'Include the result of the revised review.'
  const steerReply = 'The revised review found two open items.'
  const gate = 'codewhale-cli-steer-stream'
  const approvedFile = join(workingDir, 'cli-steer-permission.txt')
  const lead = [
    'agent',
    'send',
    agentId,
    modelScript.prompt('Check the original review.'),
    '--request-id',
    'codewhale-cli-lead',
  ]
  const steer = [
    'agent',
    'send',
    agentId,
    modelScript.prompt(steerPrompt),
    '--request-id',
    'codewhale-cli-steer',
  ]

  await withCleanup(async () => {
    const first = await modelScript.queue(
      {
        ...nativeTextStep(native, leadReply),
        stream: {
          chunkChars: 12,
          delayMs: 0,
          gates: [
            { afterChunk: 1, name: gate },
          ],
        },
      },
      {
        toolCalls: [
          bashToolCall(native.provider, 'cli-steer-permission', 'touch cli-steer-permission.txt'),
        ],
      },
      nativeTextStep(native, steerReply),
    )
    await runCLI(cli, lead)
    await modelScript.waitForGate(gate)
    const queued = await runCLI(cli, steer) as { input_id: string }
    const row = queuedInputRow(page, steerPrompt)
    await steerButton(row).click()
    await expectSteerAccepted(page, row)
    await modelScript.releaseGateIfHeld(gate)
    await modelScript.waitForSteps(first + 2)
    expect(nativeScenarioModelContextText(native, await modelScript.requestAt(first + 1))).toContain(steerPrompt)
    await expect(await waitForControlBanner(page)).toContainText('cli-steer-permission.txt')
    expect(existsSync(approvedFile)).toBe(false)
    expect(await runCLI(cli, [...steer, '--wait', '--all'])).toMatchObject({
      input_id: queued.input_id,
      message: 'delivered',
      state: 'waiting_for_user',
      replies: [],
    })
    await approveCodewhalePermission(cli, agentId)
    await modelScript.waitForSteps(first + 3)
    await waitForAgentIdle(page)

    expect(existsSync(approvedFile)).toBe(true)
    await expect(userBubbles(page).filter({ hasText: steerPrompt })).toHaveCount(1)
    await expect(assistantBubbles(page).filter({ hasText: steerReply })).toHaveCount(1)
    expect(await runCLI(cli, [...steer, '--wait', '--all'])).toMatchObject({
      input_id: queued.input_id,
      message: 'delivered',
      turn_outcome: 'completed',
      replies: [steerReply],
    })
    expect(await runCLI(cli, [...lead, '--wait', '--all'])).toMatchObject({
      turn_outcome: 'completed',
      replies: [leadReply, steerReply],
    })
  }, async () => { await modelScript.releaseGateIfHeld(gate) })
})

codewhaleTest('keeps an exact native reply readable while a later input waits for permission', async ({ native, leapmuxServer, modelScript, page }) => {
  const cli = await mintCLITokenForAdmin(leapmuxServer)
  const { id: agentId, workingDir } = await currentNativeAgent(native)
  const firstPrompt = 'Summarize the CodeWhale readiness check.'
  const firstReply = 'The CodeWhale readiness check completed.'
  await modelScript.queue({ text: firstReply })
  const first = sendAndWait(agentId, modelScript.prompt(firstPrompt), 'codewhale-readiness')
  const completed = await runCLI(cli, first) as { input_id: string }
  expect(completed).toMatchObject({
    message: 'delivered',
    turn_outcome: 'completed',
    replies: [firstReply],
  })

  const secondReply = 'The approved file now exists.'
  const file = join(workingDir, 'cli-permission.txt')
  await modelScript.queue({
    toolCalls: [
      bashToolCall(native.provider, 'cli-permission-write', 'touch cli-permission.txt'),
    ],
  }, { text: secondReply })
  const second = sendAndWait(agentId, modelScript.prompt('Run the requested file creation.'), 'codewhale-permission')
  const waiting = await runCLI(cli, second) as { input_id: string }
  expect(waiting).toMatchObject({ message: 'delivered', state: 'waiting_for_user', replies: [] })
  await waitForControlBanner(page)
  expect(existsSync(file)).toBe(false)
  expect(await runCLI(cli, first)).toMatchObject({
    input_id: completed.input_id,
    turn_outcome: 'completed',
    replies: [firstReply],
  })
  await expect(userBubbles(page).filter({ hasText: firstPrompt })).toHaveCount(1)

  await approveCodewhalePermission(cli, agentId)
  expect(await runCLI(cli, second)).toMatchObject({
    input_id: waiting.input_id,
    turn_outcome: 'completed',
    replies: [secondReply],
  })
  expect(existsSync(file)).toBe(true)
  await expect(assistantBubbles(page).filter({ hasText: secondReply })).toHaveCount(1)
})
