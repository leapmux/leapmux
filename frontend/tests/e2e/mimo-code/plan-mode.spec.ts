import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { nativeModelInstructionText } from '../helpers/nativeScenario'
import { exerciseNativeOption } from '../helpers/nativeSettings'
import { mimoTest } from '../mimo-fixtures'

mimoTest('enables native Plan mode from its setting before and after reload', async ({ page, modelScript, leapmuxServer, authenticatedMiMoWorkspace }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedMiMoWorkspace.workspaceId, provider: AgentProvider.MIMO_CODE }
  await exerciseNativeOption(context, { groupId: 'permissionMode', value: 'plan', nativeProof: (request) => {
    const instruction = nativeModelInstructionText(request)
    expect(instruction).toMatch(/Plan mode is (?:still )?active/i)
    expect(instruction).toContain('read-only')
  } })
})
