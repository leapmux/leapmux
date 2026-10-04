import { existsSync, mkdirSync, writeFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import process from 'node:process'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { agentOpenOptions, agentSettings } from '../agentSettings'
import { codebuddyTest } from '../codebuddy-fixtures'
import { openAgentViaAPI } from '../helpers/api'
import { expectNoNativeStartupControl } from '../helpers/nativeControlObservation'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { createTestDirectory } from '../helpers/runDirectory'
import { tabById } from '../helpers/ui'
import { createGitRepo } from '../helpers/worktree'
import { nativeContext } from './scenarios'

codebuddyTest('starts with a real project configuration and no native workspace trust barrier', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  const workingDir = createGitRepo(createTestDirectory('codebuddy-code-trust-'), 'repo')
  const marker = join(workingDir, 'workspace-configuration-marker')
  const program = join(workingDir, 'workspace-hook.cjs')
  writeFileSync(program, `require('node:fs').writeFileSync(${JSON.stringify(marker)},'WORKSPACE_CONFIG_EXECUTED');process.stdout.write(JSON.stringify({continue:true}))`)
  const settings = join(workingDir, '.codebuddy', 'settings.json')
  mkdirSync(dirname(settings), { recursive: true })
  writeFileSync(settings, JSON.stringify({ hooks: { SessionStart: [{ hooks: [{ type: 'command', command: `${JSON.stringify(process.execPath)} ${JSON.stringify(program)}` }] }] } }))
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
  await expectNoNativeStartupControl(context, {
    testId: 'control-banner',
    additionalTestIds: ['dialog-editor'],
    start: async () => {
      const agentId = await openAgentViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, authenticatedEmptyWorkspace.workspaceId, workingDir, {
        agentProvider: AgentProvider.CODEBUDDY,
        ...agentOpenOptions(agentSettings(AgentProvider.CODEBUDDY)),
      })
      await tabById(page, agentId).click()
    },
    relatedControl: async () => {
      await sendNativeAnswer(context, 'Return one native response from this scratch project.', 'The native project configuration probe completed.')
      expect(existsSync(marker)).toBe(false)
    },
  })
})
