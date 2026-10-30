import { expect } from '@playwright/test'
import { MOCK_MODELS, MOCK_PROVIDER_IDS } from '../helpers/mockAgentEnvironment'
import { resumePickerScenario } from '../helpers/nativeResumePicker'
import { nativeOptionGroup, nativeOptionValue } from '../helpers/nativeScenario'
import { chooseNativeModel } from '../helpers/nativeSettings'
import { revokeZcodeModelFromConfiguration, withZcodeConfigurationRestored } from '../helpers/zcodeEnvironment'
import { zcodeTest } from '../zcode-fixtures'
import { nativeContext } from './scenarios'

const label = 'ZCode'

zcodeTest('restores old Worker rows and native model context after reopening', async ({ page, leapmuxServer, modelScript }) => {
  await resumePickerScenario({ page, leapmuxServer, modelScript }, nativeContext, { label })
})

// Run before the workspace fixture starts the agent. Restore the private files
// even when the scenario fails, because the shard shares its HOME.
const revokedModelResumeTest = zcodeTest.extend<{ revokedModelConfiguration: void }>({
  revokedModelConfiguration: [
    async ({ leapmuxServer }, use) => {
      await withZcodeConfigurationRestored(leapmuxServer.agentEnv.HOME!, use)
    },
    { auto: true },
  ],
})

// The user revokes the model of the stored session while its agent is closed. The
// resumed session keeps the unavailable selection until the user chooses a live model.
revokedModelResumeTest('requires an explicit model choice after reopening a session whose model was revoked', async ({ page, leapmuxServer, modelScript }) => {
  await resumePickerScenario({ page, leapmuxServer, modelScript }, nativeContext, {
    label,
    onFirstTurn(request) {
      expect(request.body).toMatchObject({ model: MOCK_MODELS.zai })
      revokeZcodeModelFromConfiguration(leapmuxServer.agentEnv.HOME!, MOCK_MODELS.zai)
    },
    async beforeResumedPrompt(context, reopened) {
      const replacement = `${MOCK_PROVIDER_IDS.zcode}/${MOCK_MODELS.pi}`
      expect(nativeOptionValue(reopened, 'model')).not.toBe(replacement)
      const available = nativeOptionGroup(reopened, 'model')?.options.map(option => option.id)
      expect(available).toContain(replacement)
      expect(available).not.toContain(`${MOCK_PROVIDER_IDS.zcode}/${MOCK_MODELS.zai}`)
      await chooseNativeModel(context, replacement)
    },
    onResumedRequest(request) {
      expect(request.body).toMatchObject({ model: MOCK_MODELS.pi })
    },
  })
})
