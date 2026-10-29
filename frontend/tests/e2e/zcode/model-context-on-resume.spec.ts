import { readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { MOCK_MODELS } from '../helpers/mockAgentEnvironment'
import { resumePickerScenario } from '../helpers/nativeResumePicker'
import { revokeZcodeModelFromConfiguration } from '../helpers/zcodeEnvironment'
import { zcodeTest } from '../zcode-fixtures'
import { nativeContext } from './scenarios'

const label = 'ZCode'

zcodeTest('restores old Worker rows and native model context after reopening', async ({ page, leapmuxServer, modelScript }) => {
  await resumePickerScenario({ page, leapmuxServer, modelScript }, nativeContext, { label })
})

// Run before the workspace fixture starts the agent. Restore the private files
// even when the scenario fails, because the shard shares its HOME.
const revokedModelResumeTest = zcodeTest.extend<{ revokedModelConfiguration: void }>({
  revokedModelConfiguration: [async ({ leapmuxServer }, use) => {
    const dir = join(leapmuxServer.agentEnv.HOME!, '.zcode', 'v2')
    const originals = new Map(['config.json', 'provider_config.json'].map((name) => {
      const path = join(dir, name)
      return [path, readFileSync(path)] as const
    }))
    try {
      await use()
    }
    finally {
      for (const [path, original] of originals)
        writeFileSync(path, original, { mode: 0o600 })
    }
  }, { auto: true }],
})

// The user revokes the model of the stored session while its agent is closed. The
// resumed session must not survive on the deleted model: the repair selects the
// first live model, and the resumed turn reaches the model through it.
revokedModelResumeTest('runs a live model after reopening a session whose model was revoked', async ({ page, leapmuxServer, modelScript }) => {
  await resumePickerScenario({ page, leapmuxServer, modelScript }, nativeContext, {
    label,
    onFirstTurn(request) {
      expect(request.body).toMatchObject({ model: MOCK_MODELS.zai })
      revokeZcodeModelFromConfiguration(leapmuxServer.agentEnv.HOME!, MOCK_MODELS.zai)
    },
    onResumedRequest(request) {
      expect(request.body).toMatchObject({ model: MOCK_MODELS.pi })
    },
  })
})
