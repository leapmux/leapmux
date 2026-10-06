import { existsSync, readFileSync } from 'node:fs'
import { delimiter, join } from 'node:path'
import { expect } from '@playwright/test'
import { CREDENTIAL_STORE_SHIM_LOG, MODEL_KEY } from '../helpers/mockAgentEnvironment'
import { exerciseCredentialIsolation } from '../helpers/nativeCredentialIsolation'
import { junieAnswerToolCall } from '../helpers/providerToolCalls'
import { loginViaToken, openWorkspace, sendMessage } from '../helpers/ui'
import { newProviderWorkingDir, openProviderAgent } from '../helpers/workspace'
import { junieTest } from '../junie-fixtures'
import { JUNIE_AGENT } from './scenarios'

junieTest.describe('native credential isolation', () => {
  junieTest.skip(process.platform !== 'darwin', 'the system keychain probe exists on macOS')

  junieTest('uses the private credential stub during a native turn', async ({ authenticatedEmptyWorkspace, leapmuxServer, page, modelScript }) => {
    const shimDir = leapmuxServer.agentEnv.PATH?.split(delimiter)[0]
    if (!shimDir)
      throw new Error('the Junie test needs a private credential stub')
    const logPath = join(shimDir, CREDENTIAL_STORE_SHIM_LOG)
    const count = () => existsSync(logPath) ? readFileSync(logPath, 'utf8').split('\n').filter(Boolean).length : 0
    const before = count()

    const workingDir = newProviderWorkingDir(JUNIE_AGENT, 'junie-credential-stub-')
    await openProviderAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, JUNIE_AGENT, { workingDir })
    await loginViaToken(page, leapmuxServer.adminToken)
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    const step = await modelScript.queue({ toolCalls: [junieAnswerToolCall('junie-credential-answer', 'The isolated turn completed.')] })
    await sendMessage(page, modelScript.prompt('Reply once through the isolated Junie process.'))
    await modelScript.waitForSteps(step + 1)

    await expect.poll(count).toBeGreaterThan(before)
    expect(readFileSync(logPath, 'utf8').trim().split('\n').every(line => line === 'security')).toBe(true)
  })
})

junieTest('runs the actual native turn with private configuration and mock credentials', async ({ native, leapmuxServer }) => {
  const environment = leapmuxServer.agentEnv
  const home = environment.JUNIE_HOME
  if (!home)
    throw new Error('The private provider configuration is absent.')
  const configurationPath = environment.JUNIE_CONFIG_LOCATION
  if (!configurationPath)
    throw new Error('The private Junie model configuration is absent.')
  await exerciseCredentialIsolation(native, {
    privateDirectories: [home],
    expectedCredential: MODEL_KEY,
    configurationFiles: [configurationPath],
    configurationMarkers: ['leapmux-e2e-openai'],
  })
})
