import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { isObject } from '../../../src/lib/jsonPick'
import { withCleanup } from '../helpers/cleanup'
import { mintCLITokenForAdmin, runCLI } from '../helpers/cli'
import { openNativeAgent } from '../helpers/nativeAgentOpen'
import { withNativeConfigurationFile } from '../helpers/nativeConfigurationFile'
import { chooseQuestionOption } from '../helpers/nativeQuestion'
import { currentNativeAgent, nativeScenarioModelContextText } from '../helpers/nativeScenario'
import { askUserQuestionToolCall } from '../helpers/providerToolCalls'
import { getGlobalState } from '../helpers/server'
import { expectSteerAccepted, steerButton } from '../helpers/steer'
import { inputQueue, userBubbles, waitForAgentIdle, waitForControlBanner } from '../helpers/ui'
import { piTest } from '../pi-fixtures'
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
  '--timeout',
  '20s',
]

function sendAndWait(agentId: string, prompt: string, requestId: string): string[] {
  return keyedSend(agentId, prompt, requestId).concat(waitFlags)
}

piTest('returns the original reply while a later input waits for an answer', async ({ native, leapmuxServer, modelScript, page }) => {
  const cli = await mintCLITokenForAdmin(leapmuxServer)
  const { id: agentId } = await currentNativeAgent(native)
  const prompt = 'Summarize the initial readiness check.'
  const answer = 'The initial readiness check completed.'
  await modelScript.queue({ text: answer })
  const first = sendAndWait(agentId, modelScript.prompt(prompt), 'pi-readiness')
  const completed = await runCLI(cli, first) as { input_id: string }
  expect(completed).toMatchObject({
    message: 'delivered',
    turn_outcome: 'completed',
    replies: [answer],
  })

  const nextAnswer = 'The native question was answered.'
  const question = askUserQuestionToolCall(native.provider, 'pi-cli-question', [
    {
      question: 'Which route should I use?',
      header: 'Route',
      options: [
        { label: 'First', description: 'Use the first route.' },
        { label: 'Second', description: 'Use the second route.' },
      ],
    },
  ])
  const step = await modelScript.queue({
    toolCalls: [question],
  }, { text: nextAnswer })
  const second = sendAndWait(agentId, modelScript.prompt('Ask the scripted route question.'), 'pi-question')
  const waiting = await runCLI(cli, second) as { input_id: string }
  expect(waiting).toMatchObject({ message: 'delivered', state: 'waiting_for_user', replies: [] })
  const banner = await waitForControlBanner(page)
  expect(await runCLI(cli, first)).toMatchObject({
    input_id: completed.input_id,
    turn_outcome: 'completed',
    replies: [answer],
  })
  await chooseQuestionOption('Second')(banner)
  await modelScript.waitForSteps(step + 2)
  expect(await runCLI(cli, second)).toMatchObject({
    input_id: waiting.input_id,
    turn_outcome: 'completed',
    replies: [nextAnswer],
  })
  await expect(userBubbles(page).filter({ hasText: prompt })).toHaveCount(1)
})

piTest('keeps exact replies to identical steers on their own request IDs', async ({ native }) => {
  await exerciseIdenticalSteers(native, false)
})

piTest('returns the same exact reply for two steers Pi consumes together', async ({ page, modelScript, leapmuxServer, authenticatedEmptyWorkspace }) => {
  const configRoot = leapmuxServer.agentEnv.PI_CODING_AGENT_DIR
  if (!configRoot)
    throw new Error('Pi needs an isolated agent directory for the steering mode fixture.')
  const path = join(configRoot, 'settings.json')
  const defaults: unknown = JSON.parse(readFileSync(path, 'utf8'))
  if (!isObject(defaults))
    throw new Error('Pi needs its isolated settings before applying the steering mode.')
  await withNativeConfigurationFile({
    path,
    content: JSON.stringify({
      ...defaults,
      steeringMode: 'all',
    }),
    runDir: getGlobalState().tmpDir,
  }, async () => {
    const native = await nativeContext({
      page,
      modelScript,
      leapmuxServer,
      workspaceId: authenticatedEmptyWorkspace.workspaceId,
    })
    await openNativeAgent(native, { directoryPrefix: 'pi-batched-steer-' })
    await exerciseIdenticalSteers(native, true)
  })
})

async function exerciseIdenticalSteers(native: ManagedNativeScenarioContext, batched: boolean): Promise<void> {
  const { leapmuxServer, modelScript, page } = native
  const cli = await mintCLITokenForAdmin(leapmuxServer)
  const { id: agentId } = await currentNativeAgent(native)
  const leadGate = 'hold-pi-lead-before-steer'
  const leadReply = 'The release list has two candidates.'
  const firstGate = 'hold-pi-first-steer-before-second-steer'
  const steerPrompt = 'Check the next release candidate.'
  const secondReply = 'The second candidate is ready.'
  const firstReply = batched ? secondReply : 'The first candidate needs a change.'
  const prompt = modelScript.prompt(steerPrompt)
  const lead = keyedSend(agentId, modelScript.prompt('Check the release list.'), 'pi-lead-before-steer')
  const first = keyedSend(agentId, prompt, 'pi-first-identical-steer')
  const second = keyedSend(agentId, prompt, 'pi-second-identical-steer')
  const enqueueSteer = async (args: string[]) => {
    const { input_id: inputId } = await runCLI(cli, args) as { input_id: string }
    const row = inputQueue(page).getByTestId(`queued-input-${inputId}`)
    await steerButton(row).click()
    await expectSteerAccepted(page, row)
    return inputId
  }

  await withCleanup(async () => {
    const continuation = batched
      ? []
      : [
          { text: firstReply, gate: firstGate },
        ]
    const start = await modelScript.queue(
      { text: leadReply, gate: leadGate },
      ...continuation,
      { text: secondReply },
    )
    const leadInput = await runCLI(cli, lead) as { input_id: string }
    await modelScript.waitForGate(leadGate)
    const firstInput = await enqueueSteer(first)
    if (!batched) {
      await modelScript.releaseGate(leadGate)
      await modelScript.waitForGate(firstGate)
      expect(await runCLI(cli, lead.concat(waitFlags, '--all'))).toMatchObject({
        input_id: leadInput.input_id,
        state: 'working',
        turn_outcome: 'completed',
        replies: [leadReply],
      })
    }
    const secondInput = await enqueueSteer(second)
    expect(secondInput).not.toBe(firstInput)
    await modelScript.releaseGate(batched ? leadGate : firstGate)
    await modelScript.waitForSteps(start + (batched ? 2 : 3))
    const firstModelInput = nativeScenarioModelContextText(native, await modelScript.requestAt(start + 1))
    expect(firstModelInput.split(steerPrompt)).toHaveLength(batched ? 3 : 2)
    expect(firstModelInput).not.toContain('leapmux-steer:')
    await waitForAgentIdle(page)

    expect(await runCLI(cli, lead.concat(waitFlags, '--all'))).toMatchObject({
      input_id: leadInput.input_id,
      turn_outcome: 'completed',
      replies: [leadReply],
    })
    expect(await runCLI(cli, first.concat(waitFlags))).toMatchObject({
      input_id: firstInput,
      turn_outcome: 'completed',
      replies: [firstReply],
    })
    expect(await runCLI(cli, second.concat(waitFlags))).toMatchObject({
      input_id: secondInput,
      turn_outcome: 'completed',
      replies: [secondReply],
    })
    await expect(userBubbles(page).filter({ hasText: steerPrompt })).toHaveCount(2)
  }, async () => {
    await modelScript.releaseGateIfHeld(leadGate)
    await modelScript.releaseGateIfHeld(firstGate)
  })
}
