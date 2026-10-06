import { existsSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import process from 'node:process'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { agentOpenOptions, agentSettings } from '../agentSettings'
import { fastAgentTest } from '../fastagent-fixtures'
import { openAgentViaAPI } from '../helpers/api'
import { writeMcpEchoServer } from '../helpers/mcpEchoServer'
import { expectNoNativeStartupControl } from '../helpers/nativeControlObservation'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { nativeModelToolNames } from '../helpers/nativeScenario'
import { createTestDirectory } from '../helpers/runDirectory'
import { tabById } from '../helpers/ui'
import { createGitRepo } from '../helpers/worktree'
import { nativeContext } from './scenarios'

fastAgentTest('starts with a real project configuration and no native workspace trust barrier', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  const workingDir = createGitRepo(createTestDirectory('fast-agent-trust-'), 'repo')
  const receiptLog = join(workingDir, 'workspace-mcp-receipt.json')
  const script = writeMcpEchoServer(workingDir, { receiptLog })
  writeFileSync(join(workingDir, 'fast-agent.yaml'), `mcp:\n  servers:\n    trust_probe:\n      command: ${JSON.stringify(process.execPath)}\n      args: [${JSON.stringify(script)}]\n`)
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
  await expectNoNativeStartupControl(context, {
    testId: 'control-banner',
    additionalTestIds: ['dialog-editor'],
    start: async () => {
      const agentId = await openAgentViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, authenticatedEmptyWorkspace.workspaceId, workingDir, {
        agentProvider: AgentProvider.FAST_AGENT,
        ...agentOpenOptions(agentSettings(AgentProvider.FAST_AGENT)),
      })
      await tabById(page, agentId).click()
    },
    relatedProof: async () => {
      const request = await sendNativeAnswer(context, 'Return one native response from this scratch project.', 'The native project configuration probe completed.')
      expect(nativeModelToolNames(request).some(name => name.includes('trust_probe'))).toBe(false)
      expect(existsSync(receiptLog)).toBe(false)
    },
  })
})
