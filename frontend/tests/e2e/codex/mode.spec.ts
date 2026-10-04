import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { codexTest } from '../codex-fixtures'
import { nativeModelInstructionText } from '../helpers/nativeScenario'
import { exerciseNativeOption } from '../helpers/nativeSettings'

codexTest('applies and restores the selected native collaboration mode', async ({ authenticatedCodexWorkspace, page, leapmuxServer, modelScript }) => {
  await exerciseNativeOption({ page, modelScript, leapmuxServer, provider: AgentProvider.CODEX, workspaceId: authenticatedCodexWorkspace.workspaceId }, {
    groupId: 'collaboration_mode',
    value: 'plan',
    nativeProof: (request) => {
      expect(request.protocol).toBe('openai-responses')
      const instructions = nativeModelInstructionText(request)
      expect(instructions).toContain('<collaboration_mode>')
      expect(instructions).toMatch(/plan mode/i)
    },
  })
})
