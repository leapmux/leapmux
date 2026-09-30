import { existsSync, readFileSync } from 'node:fs'
import { delimiter, join } from 'node:path'
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { agentOpenOptions, agentSettings } from './agentSettings'
import { openAgentViaAPI } from './helpers/api'
import { CREDENTIAL_STORE_SHIM_LOG } from './helpers/mockAgentEnvironment'
import { junieAnswerToolCall } from './helpers/providerToolCalls'
import { createTestDirectory } from './helpers/runDirectory'
import { loginViaToken, openWorkspace, sendMessage } from './helpers/ui'
import { expect, JUNIE_E2E_SKIP_REASON, junieTest } from './junie-fixtures'

junieTest.skip(!!JUNIE_E2E_SKIP_REASON, JUNIE_E2E_SKIP_REASON || '')
junieTest.skip(process.platform !== 'darwin', 'the system keychain probe exists on macOS')

junieTest('uses the private credential stub during a native turn', async ({ authenticatedEmptyWorkspace, leapmuxServer, page, modelScript }) => {
  const shimDir = leapmuxServer.agentEnv.PATH?.split(delimiter)[0]
  if (!shimDir)
    throw new Error('the Junie test needs a private credential stub')
  const logPath = join(shimDir, CREDENTIAL_STORE_SHIM_LOG)
  const count = () => existsSync(logPath) ? readFileSync(logPath, 'utf8').split('\n').filter(Boolean).length : 0
  const before = count()

  const workingDir = createTestDirectory('junie-credential-stub-')
  await openAgentViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, authenticatedEmptyWorkspace.workspaceId, workingDir, {
    agentProvider: AgentProvider.JUNIE,
    ...agentOpenOptions(agentSettings(AgentProvider.JUNIE)),
  })
  await loginViaToken(page, leapmuxServer.adminToken)
  await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
  await modelScript.rule(
    { name: 'junie-capability-filter', when: { system: 'capability filter agent' }, respond: { text: '' } },
    { name: 'junie-task-name', when: { system: 'task description summarizer' }, respond: { text: 'Credential test' } },
  )
  await modelScript.queue({ toolCalls: [junieAnswerToolCall('junie-credential-answer', 'The isolated turn completed.')] })
  await sendMessage(page, modelScript.prompt('Reply once through the isolated Junie process.'))
  await modelScript.waitForSteps()

  await expect.poll(count).toBeGreaterThan(before)
  expect(readFileSync(logPath, 'utf8').trim().split('\n').every(line => line === 'security')).toBe(true)
})
