import { expect } from '@playwright/test'
import { AgentInputState } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { codexTest } from '../codex-fixtures'
import { withCleanup } from '../helpers/cleanup'
import { mintCLITokenForAdmin, runCLI } from '../helpers/cli'
import { NATIVE_CONTROL_QUESTION } from '../helpers/nativeQuestion'
import { currentNativeAgent, nativeTextStep } from '../helpers/nativeScenario'
import { askUserQuestionToolCall, bashToolCall } from '../helpers/providerToolCalls'
import { queuedInputRow } from '../helpers/steer'
import { createToolOutputControl } from '../helpers/toolOutputControl'
import { assistantBubbles, chooseSettingsOption, userBubbles, waitForAgentIdle, waitForControlBanner, waitForSettingsIdle } from '../helpers/ui'
import { codexAsyncExecRules } from './asyncExec'

codexTest('retries the completed reply of an exact sent input', async ({ native, leapmuxServer }) => {
  const { id: agentId } = await currentNativeAgent(native)
  const cli = await mintCLITokenForAdmin(leapmuxServer)
  const question = 'Report the release checklist result.'
  const answer = 'The release checklist passed.'
  const step = await native.modelScript.queue(nativeTextStep(native, answer))
  const args = [
    'agent',
    'send',
    agentId,
    native.modelScript.prompt(question),
    '--wait',
    '--all',
    '--request-id',
    'codex-release-checklist',
  ]
  const first = await runCLI(cli, args) as { input_id: string }
  await native.modelScript.waitForSteps(step + 1)
  await waitForAgentIdle(native.page)
  expect(first).toMatchObject({
    id: agentId,
    message: 'delivered',
    turn_outcome: 'completed',
    replies: [answer],
  })
  await chooseSettingsOption(native.page, 'collaboration_mode-plan')
  await waitForSettingsIdle(native.page)
  const laterAnswer = 'The choice was recorded.'
  const laterStep = await native.modelScript.queue(
    {
      toolCalls: [
        askUserQuestionToolCall(native.provider, 'codex-cli-later-question', [NATIVE_CONTROL_QUESTION]),
      ],
    },
    nativeTextStep(native, laterAnswer),
  )
  const laterArgs = [
    'agent',
    'send',
    agentId,
    native.modelScript.prompt('Ask the scripted question.'),
    '--wait',
    '--request-id',
    'codex-later-question',
  ]
  try {
    const waiting = await runCLI(cli, laterArgs, { timeoutMs: 15_000 })
    expect(waiting).toMatchObject({ message: 'delivered', state: 'waiting_for_user' })
    const banner = await waitForControlBanner(native.page)
    await expect(banner).toContainText(NATIVE_CONTROL_QUESTION.question)
    expect(await runCLI(cli, args)).toMatchObject({
      input_id: first.input_id,
      message: 'delivered',
      turn_outcome: 'completed',
      replies: [answer],
    })
    await expect(banner).toContainText(NATIVE_CONTROL_QUESTION.question)
  }
  finally {
    const { requests } = await runCLI(cli, ['agent', 'requests', agentId]) as { requests: Array<{ id: string }> }
    expect(requests).toHaveLength(1)
    await runCLI(cli, [
      'agent',
      'answer',
      agentId,
      requests[0]!.id,
      '--option',
      '2',
    ])
    await native.modelScript.waitForSteps(laterStep + 2)
    await expect(assistantBubbles(native.page).filter({ hasText: laterAnswer })).toHaveCount(1)
  }
  await expect(userBubbles(native.page).filter({ hasText: question })).toHaveCount(1)
  await expect(assistantBubbles(native.page).filter({ hasText: answer })).toHaveCount(1)
})

codexTest('returns an exact reply after a native steer boundary', async ({ native, leapmuxServer }) => {
  const { id: agentId, workingDir } = await currentNativeAgent(native)
  const cli = await mintCLITokenForAdmin(leapmuxServer)
  const { modelScript, page } = native
  const output = createToolOutputControl(workingDir)
  const steerText = 'Include the revised checklist.'
  const before = 'I will run the checklist command.'
  const after = 'The revised checklist passed.'
  await modelScript.rule(...codexAsyncExecRules(steerText))
  await modelScript.queue({
    text: before,
    toolCalls: [
      bashToolCall(native.provider, 'codex-cli-steer-boundary', output.command),
    ],
  }, nativeTextStep(native, after))
  const root = [
    'agent',
    'send',
    agentId,
    modelScript.prompt('Run the controlled checklist command.'),
    '--request-id',
    'codex-cli-steer-root',
    '--wait',
    '--all',
    '--timeout',
    '25s',
  ]
  const steer = [
    'agent',
    'send',
    agentId,
    modelScript.prompt(steerText),
    '--request-id',
    'codex-cli-steer-input',
    '--wait',
    '--all',
    '--timeout',
    '25s',
  ]
  const deliverSteer = async () => {
    await output.waitForFirstOutput()
    return await Promise.all([
      runCLI(cli, steer),
      (async () => {
        const row = queuedInputRow(page, steerText)
        await row.getByRole('button', { name: 'Steer', exact: true }).click()
        await expect(row.and(page.locator(`:not([data-input-state="${AgentInputState.AWAITING_ECHO}"])`))).toHaveCount(0)
        await output.releaseFirstOutput()
        await output.waitForSecondOutput()
        await output.releaseFinalOutput()
      })(),
    ])
  }
  const [first, [steered]] = await withCleanup(async () => await Promise.all([
    runCLI(cli, root),
    deliverSteer(),
  ]), async () => {
    await output.releaseFirstOutput()
    await output.releaseFinalOutput()
  })
  expect(first).toMatchObject({
    turn_outcome: 'completed',
    replies: expect.arrayContaining([
      before,
      after,
    ]),
  })
  const expected = {
    input_id: (steered as { input_id: string }).input_id,
    turn_outcome: 'completed',
    replies: [
      after,
    ],
  }
  expect(steered).toMatchObject(expected)
  expect(await runCLI(cli, steer)).toMatchObject(expected)
  await expect(userBubbles(page).filter({ hasText: steerText })).toHaveCount(1)
})
