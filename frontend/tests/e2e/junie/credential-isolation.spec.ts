import { existsSync, readFileSync } from 'node:fs'
import { delimiter, join } from 'node:path'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { agentOpenOptions } from '../agentSettings'
import { openAgentViaAPI } from '../helpers/api'
import { CREDENTIAL_STORE_SHIM_LOG, MODEL_KEY } from '../helpers/mockAgentEnvironment'
import { exerciseCredentialIsolation } from '../helpers/nativeCredentialIsolation'
import { junieAnswerToolCall } from '../helpers/providerToolCalls'
import { createTestDirectory } from '../helpers/runDirectory'
import { loginViaToken, openWorkspace, sendMessage } from '../helpers/ui'
import { expect, junieTest } from '../junie-fixtures'

junieTest.describe('native credential isolation', () => {
  junieTest.skip(process.platform !== 'darwin', 'the system keychain probe exists on macOS')

  junieTest('uses the private credential stub during a native turn', async ({ authenticatedEmptyWorkspace, leapmuxServer, page, modelScript }) => {
    const shimDir = leapmuxServer.agentEnv.PATH?.split(delimiter)[0]
    if (!shimDir)
      throw new Error('the Junie test needs a private credential stub')
    const logPath = join(shimDir, CREDENTIAL_STORE_SHIM_LOG)
    const count = () => existsSync(logPath) ? readFileSync(logPath, 'utf8').split('\n').filter(Boolean).length : 0
    const before = count()

    const workingDir = createTestDirectory('junie-credential-stub-')
    await openAgentViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, authenticatedEmptyWorkspace.workspaceId, workingDir, agentOpenOptions(AgentProvider.JUNIE))
    await loginViaToken(page, leapmuxServer.adminToken)
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await modelScript.queue({ toolCalls: [junieAnswerToolCall('junie-credential-answer', 'The isolated turn completed.')] })
    await sendMessage(page, modelScript.prompt('Reply once through the isolated Junie process.'))
    await modelScript.waitForSteps()

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
