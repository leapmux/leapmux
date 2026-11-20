import { expect } from '@playwright/test'
import { mintCLITokenForAdmin, runCLI } from '../helpers/cli'
import { currentNativeAgent, nativeTextStep } from '../helpers/nativeScenario'
import { assistantBubbles, userBubbles, waitForAgentIdle } from '../helpers/ui'
import { kimiTest } from '../kimi-fixtures'

kimiTest('prints the canonical Kimi reply seen in the transcript', async ({ native, leapmuxServer }) => {
  const { id: agentId } = await currentNativeAgent(native)
  const cli = await mintCLITokenForAdmin(leapmuxServer)
  const question = 'How many release checklist items remain open?'
  const answer = 'The release checklist has three open items.'
  const step = await native.modelScript.queue(nativeTextStep(native, answer))
  const args = [
    'agent',
    'send',
    agentId,
    native.modelScript.prompt(question),
    '--wait',
    '--all',
    '--request-id',
    'kimi-release-checklist-input',
  ]
  const result = await runCLI(cli, args)
  expect(result).toMatchObject({
    id: agentId,
    message: 'delivered',
    replies: [answer],
  })
  await native.modelScript.waitForSteps(step + 1)
  await waitForAgentIdle(native.page)
  expect(await runCLI(cli, args)).toEqual(result)
  await expect(userBubbles(native.page).filter({ hasText: question })).toHaveCount(1)
  await expect(assistantBubbles(native.page).filter({ hasText: answer })).toHaveCount(1)
})
