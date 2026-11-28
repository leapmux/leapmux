import { expect } from '@playwright/test'
import { commandCodeTest } from '../command-code-fixtures'
import { withCleanup } from '../helpers/cleanup'
import { mintCLITokenForAdmin, runCLI } from '../helpers/cli'
import { currentNativeAgent, nativeScenarioModelContextText, nativeTextStep } from '../helpers/nativeScenario'
import { bashToolCall } from '../helpers/providerToolCalls'
import { expectSteerAccepted, queuedInputRow, steerButton } from '../helpers/steer'
import { assistantBubbles, userBubbles, waitForAgentIdle } from '../helpers/ui'

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

commandCodeTest('returns exact replies to identical steers read in one native drain', async ({ native, leapmuxServer }) => {
  const { page, modelScript } = native
  const { id: agentId } = await currentNativeAgent(native)
  const cli = await mintCLITokenForAdmin(leapmuxServer)
  const leadReply = 'The first checklist is ready.'
  const steerPrompt = 'Check the revised release checklist.'
  const steerReply = 'The revised checklist has two open items.'
  const gate = 'command-code-cli-steer-lead'
  const lead = [
    'agent',
    'send',
    agentId,
    modelScript.prompt('Check the first release checklist.'),
    '--request-id',
    'command-code-cli-lead',
  ]
  const steer = [
    'agent',
    'send',
    agentId,
    modelScript.prompt(steerPrompt),
  ]

  await withCleanup(async () => {
    const start = await modelScript.queue(
      {
        ...nativeTextStep(native, leadReply),
        stream: {
          chunkChars: 10,
          delayMs: 0,
          gates: [
            { afterChunk: 1, name: gate },
          ],
        },
      },
      nativeTextStep(native, steerReply),
    )
    await runCLI(cli, lead)
    await modelScript.waitForGate(gate)
    const steered: { args: string[], id: string }[] = []
    for (const requestId of ['command-code-cli-steer-first', 'command-code-cli-steer-second']) {
      const args = [...steer, '--request-id', requestId]
      const queued = await runCLI(cli, args) as { input_id: string }
      const row = queuedInputRow(page, steerPrompt).and(page.getByTestId(`queued-input-${queued.input_id}`))
      await steerButton(row).click()
      await expectSteerAccepted(page, row)
      steered.push({ args, id: queued.input_id })
    }
    expect(new Set(steered.map(input => input.id)).size).toBe(2)
    await modelScript.releaseGate(gate)
    await modelScript.waitForSteps(start + 2)
    const context = nativeScenarioModelContextText(native, await modelScript.requestAt(start + 1))
    expect(context.split(steerPrompt)).toHaveLength(3)
    await waitForAgentIdle(page)

    await expect(userBubbles(page).filter({ hasText: steerPrompt })).toHaveCount(2)
    await expect(assistantBubbles(page).filter({ hasText: steerReply })).toHaveCount(1)
    for (const input of steered) {
      expect(await runCLI(cli, [...input.args, '--wait', '--all'])).toMatchObject({
        input_id: input.id,
        message: 'delivered',
        turn_outcome: 'completed',
        replies: [steerReply],
      })
    }
    expect(await runCLI(cli, [...lead, '--wait', '--all'])).toMatchObject({
      turn_outcome: 'completed',
      replies: [leadReply, steerReply],
    })
  }, async () => { await modelScript.releaseGateIfHeld(gate) })
})

commandCodeTest('reads the original reply while a later native turn is still working', async ({ native, leapmuxServer, modelScript, page }) => {
  const cli = await mintCLITokenForAdmin(leapmuxServer)
  const { id: agentId } = await currentNativeAgent(native)
  const prompt = 'Summarize the first readiness check.'
  const answer = 'The first readiness check completed.'
  await modelScript.queue({ text: answer })
  const first = sendAndWait(agentId, modelScript.prompt(prompt), 'command-code-readiness')
  const completed = await runCLI(cli, first) as { input_id: string }
  expect(completed).toMatchObject({
    message: 'delivered',
    turn_outcome: 'completed',
    replies: [answer],
  })

  const nextAnswer = 'The native command produced the expected output.'
  const gate = 'command-code-finish-tool-turn'
  await modelScript.queue({
    toolCalls: [
      bashToolCall(native.provider, 'command-code-cli-output', 'printf command-code-output'),
    ],
  }, { text: nextAnswer, gate })
  const second = sendAndWait(agentId, modelScript.prompt('Run the scripted readiness command.'), 'command-code-tool')
  const retryWhileWorking = async () => {
    try {
      await modelScript.waitForGate(gate)
      expect(await runCLI(cli, first)).toMatchObject({
        input_id: completed.input_id,
        turn_outcome: 'completed',
        replies: [answer],
      })
    }
    finally {
      await modelScript.releaseGateIfHeld(gate)
    }
  }
  const [next] = await Promise.all([
    runCLI(cli, second),
    retryWhileWorking(),
  ])
  expect(next).toMatchObject({
    message: 'delivered',
    turn_outcome: 'completed',
    replies: [nextAnswer],
  })
  await expect(userBubbles(page).filter({ hasText: prompt })).toHaveCount(1)
  await expect(assistantBubbles(page).filter({ hasText: nextAnswer })).toHaveCount(1)
})
