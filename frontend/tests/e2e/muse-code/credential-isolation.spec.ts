import { join } from 'node:path'
import { expect } from '@playwright/test'
import { MOCK_MODELS, MODEL_KEY } from '../helpers/mockAgentEnvironment'
import { exerciseCredentialIsolation } from '../helpers/nativeCredentialIsolation'
import { museTest } from '../muse-fixtures'

museTest('loads private native configuration and uses the suite mock credential', async ({ native, leapmuxServer }) => {
  const environment = leapmuxServer.agentEnv
  if (!environment?.HOME)
    throw new Error('The Muse credential test requires the isolated HOME.')
  expect(environment.META_API_KEY).toBe(MODEL_KEY)
  expect(environment.MUSE_NO_AUTO_UPDATE).toBe('1')
  const configurationDirectory = join(environment.HOME, '.config', 'muse')
  await exerciseCredentialIsolation(native, {
    configurationFiles: [join(configurationDirectory, 'settings.json')],
    privateDirectories: [configurationDirectory],
    absentFromConfiguration: [MODEL_KEY],
    configurationMarkers: [
      '"provider": "meta"',
      `"model": "${MOCK_MODELS.muse}"`,
      '"auth": "bearer"',
      '"foreign_personal_rules": false',
      '"foreign_personal_skills": false',
      '"max_retries": 0',
    ],
  })
})
