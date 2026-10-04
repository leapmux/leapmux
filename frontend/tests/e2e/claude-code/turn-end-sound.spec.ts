import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { claudeTest } from '../claude-fixtures'
import { exerciseTurnEndSound } from '../helpers/nativeTurnEndSound'
import { bashToolCall } from '../helpers/providerToolCalls'
import { applyPermissionPreset } from '../helpers/ui'

for (const sound of ['ding-dong', 'none'] as const) {
  claudeTest(`plays the selected sound once after native tool activity with preference ${sound}`, async ({ authenticatedClaudeWorkspace, page, leapmuxServer, modelScript }) => {
    await exerciseTurnEndSound({ page, modelScript, leapmuxServer, provider: AgentProvider.CLAUDE_CODE, workspaceId: authenticatedClaudeWorkspace.workspaceId }, {
      sound,
      toolActivity: true,
      prepare: () => applyPermissionPreset(page, 'bypass'),
      steps: [{ toolCalls: [bashToolCall(AgentProvider.CLAUDE_CODE, 'native-sound-shell', 'pwd')] }, { text: 'The native shell turn ended.' }],
    })
  })
}

claudeTest('stays quiet after a native turn without tool activity', async ({ authenticatedClaudeWorkspace, page, leapmuxServer, modelScript }) => {
  await exerciseTurnEndSound({ page, modelScript, leapmuxServer, provider: AgentProvider.CLAUDE_CODE, workspaceId: authenticatedClaudeWorkspace.workspaceId }, { toolActivity: false })
})
