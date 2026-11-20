import { expect } from '@playwright/test'
import { codexTest } from '../codex-fixtures'
import { mintCLITokenForAdmin, runCLI } from '../helpers/cli'
import { NATIVE_CONTROL_QUESTION } from '../helpers/nativeQuestion'
import { currentNativeAgent, nativeTextStep } from '../helpers/nativeScenario'
import { askUserQuestionToolCall } from '../helpers/providerToolCalls'
import { assistantBubbles, chooseSettingsOption, userBubbles, waitForAgentIdle, waitForControlBanner, waitForSettingsIdle } from '../helpers/ui'

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
