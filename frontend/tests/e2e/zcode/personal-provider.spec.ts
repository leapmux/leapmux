import { readFileSync, unlinkSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { openAgentViaAPI } from '../helpers/api'
import { MOCK_MODELS, MOCK_PROVIDER_IDS } from '../helpers/mockAgentEnvironment'
import { exerciseBasicChat } from '../helpers/nativeConversation'
import { nativeTextStep } from '../helpers/nativeScenario'
import { agentTabs, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { refreshZcodeDefaultModel } from '../helpers/zcodeEnvironment'
import { zcodeTest } from '../zcode-fixtures'

// Run before the workspace fixture starts the agent. Restore this private file
// even when startup or the conversation fails, because the shard shares its HOME.
const personalProviderTest = zcodeTest.extend<{ personalOnlyConfiguration: void }>({
  personalOnlyConfiguration: [
    async ({ leapmuxServer }, use) => {
      const path = join(leapmuxServer.agentEnv.HOME!, '.zcode', 'v2', 'config.json')
      const legacy = readFileSync(path)
      unlinkSync(path)
      try {
        await use()
      }
      finally {
        writeFileSync(path, legacy, { mode: 0o600 })
      }
    },
    { auto: true },
  ],
})

personalProviderTest('starts and completes a native turn with only personal provider configuration', async ({ native }) => {
  await exerciseBasicChat(native, { timedDivider: true })
})

// The user refreshes the personal provider configuration while the workspace is
// open: the default model moves to the second model. The next agent to start runs
// the refreshed configuration, through the same registry refresh that every start
// performs.
personalProviderTest('serves the refreshed personal-provider configuration to the next agent', async ({ native, page, leapmuxServer, modelScript, providerWorkspace }) => {
  const firstStep = await modelScript.queue(nativeTextStep(native, 'the first answer'))
  await sendMessage(page, modelScript.prompt('the first prompt'))
  await modelScript.waitForSteps(firstStep + 1)
  await waitForAgentIdle(page)
  expect((await modelScript.requestAt(firstStep)).body).toMatchObject({ model: MOCK_MODELS.zai })

  refreshZcodeDefaultModel(leapmuxServer.agentEnv.HOME!, MOCK_PROVIDER_IDS.zcode, MOCK_MODELS.pi)

  await openAgentViaAPI(leapmuxServer, providerWorkspace.workspaceId, providerWorkspace.workingDir, {
    agentProvider: native.provider,
    title: 'Refreshed',
  })
  await agentTabs(page).filter({ hasText: 'Refreshed' }).first().click()
  const secondStep = await modelScript.queue(nativeTextStep(native, 'the second answer'))
  await sendMessage(page, modelScript.prompt('the second prompt'))
  await modelScript.waitForSteps(secondStep + 1)
  await waitForAgentIdle(page)
  expect((await modelScript.requestAt(secondStep)).body).toMatchObject({ model: MOCK_MODELS.pi })
})
