import { expect } from '@playwright/test'
import { codebuddyTest } from '../codebuddy-fixtures'
import { withCleanup } from '../helpers/cleanup'
import { mintCLITokenForAdmin, runCLI } from '../helpers/cli'
import { exerciseExactCLIReply } from '../helpers/nativeExactCLIReply'
import { currentNativeAgent, nativeTextStep } from '../helpers/nativeScenario'
import { bashToolCall } from '../helpers/providerToolCalls'
import { expectSteerAccepted, queuedInputRow, steerButton } from '../helpers/steer'
import { waitForAgentIdle } from '../helpers/ui'

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

codebuddyTest('retries the exact reply while a later native question is open', async ({ native }) => {
  await exerciseExactCLIReply(native)
})

codebuddyTest('returns only the post-steer reply for a mid-turn root steer', async ({ native }) => {
  const { page, modelScript, leapmuxServer } = native
  const { id: agentId } = await currentNativeAgent(native)
  const cli = await mintCLITokenForAdmin(leapmuxServer)
  const gate = 'codebuddy-cli-steer-exact-reply'
  const beforeReply = 'Running the initial CodeBuddy release check.'
  const afterReply = 'Included the changelog summary after the steer.'
  const guidance = 'Also include the changelog summary.'
  const lead = keyedSend(agentId, modelScript.prompt('Run the CodeBuddy release check.'), 'codebuddy-steer-lead')
  const steer = keyedSend(agentId, modelScript.prompt(guidance), 'codebuddy-steer-followup')

  await withCleanup(async () => {
    const start = await modelScript.queue(
      {
        gate,
        text: beforeReply,
        toolCalls: [
          bashToolCall(native.provider, 'codebuddy-cli-steer-shell', 'printf codebuddy-steer-ready'),
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
