import { existsSync } from 'node:fs'
import { join } from 'node:path'
import process from 'node:process'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { agentOpenOptions, agentSettings } from '../agentSettings'
import { openAgentViaAPI } from '../helpers/api'
import { writeJunieMcpConfig } from '../helpers/junieMcp'
import { writeMcpEchoServer } from '../helpers/mcpEchoServer'
import { readMcpServerReceipt } from '../helpers/mcpServerReceipt'
import { expectNoNativeStartupControl } from '../helpers/nativeControlObservation'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { createTestDirectory } from '../helpers/runDirectory'
import { tabById } from '../helpers/ui'
import { createGitRepo } from '../helpers/worktree'
import { junieTest } from '../junie-fixtures'
import { nativeContext } from './scenarios'

junieTest('starts with a real project configuration and no native workspace trust barrier', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  const workingDir = createGitRepo(createTestDirectory('junie-trust-'), 'repo')
  const receiptLog = join(workingDir, 'workspace-mcp-receipt.json')
  const script = writeMcpEchoServer(workingDir, { receiptLog })
  writeJunieMcpConfig(workingDir, 'trust_probe', process.execPath, [script])
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
  await expectNoNativeStartupControl(context, {
    testId: 'control-banner',
    additionalTestIds: ['dialog-editor'],
    start: async () => {
      const agentId = await openAgentViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, authenticatedEmptyWorkspace.workspaceId, workingDir, {
        agentProvider: AgentProvider.JUNIE,
        ...agentOpenOptions(agentSettings(AgentProvider.JUNIE)),
      })
      await tabById(page, agentId).click()
    },
    relatedControl: async () => {
      await sendNativeAnswer(context, 'Return one native response from this scratch project.', 'The native project configuration probe completed.')
      await expect.poll(() => existsSync(receiptLog)).toBe(true)
      const receipt = readMcpServerReceipt(receiptLog)
      expect(receipt.initializeCapabilities).not.toBeNull()
      expect(receipt.toolCatalogs.flatMap(catalog => catalog.tools.map(tool => tool.name))).toContain('echo')
    },
  })
})
