import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { COPILOT_E2E_SKIP_REASON, copilotTest } from '../copilot-fixtures'
import { nativeModelToolNames } from '../helpers/nativeScenario'
import { spawnSubagentToolCall } from '../helpers/providerToolCalls'
import { openRunningNativeChild } from '../helpers/runningChildProof'
import { applyPermissionPreset } from '../helpers/ui'
import { exerciseUngroupedNativeChildren } from '../helpers/ungroupedNativeChildren'
import { readCopilotChildTaskId } from './childIdentity'

copilotTest.skip(!!COPILOT_E2E_SKIP_REASON, COPILOT_E2E_SKIP_REASON || '')

copilotTest('keeps two actual native children outside workflow groups after reload', async ({ authenticatedCopilotWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedCopilotWorkspace.workspaceId, provider: AgentProvider.GITHUB_COPILOT }
  const suffix = crypto.randomUUID().replaceAll('-', '')
  await exerciseUngroupedNativeChildren(context, {
    openChild: async (index) => {
      const gate = `group-child-${index}-${suffix}`
      const description = `Actual grouping child ${index} ${suffix}`
      const prompt = modelScript.prompt(`NATIVE_GROUP_CHILD_${index}_${suffix}: reply once.`)
      const spawn = spawnSubagentToolCall(AgentProvider.GITHUB_COPILOT, `native-group-child-${index}`, { description, prompt })
      return openRunningNativeChild(context, {
        spawn,
        resolveTaskId: parentId => readCopilotChildTaskId(context, parentId, spawn.id),
        gate,
        childMatcher: { user: `(?:^|\\n)NATIVE_GROUP_CHILD_${index}_${suffix}` },
        childFinalStep: { text: `ACTUAL_CHILD_REPORT_${index}_${suffix}` },
        allowExistingRows: index > 0,
        rowText: description,
        prepare: () => applyPermissionPreset(page, 'bypass'),
      })
    },
    nativeCatalogProof: async () => {
      const parentRequest = (await modelScript.status()).requests.find(record => record.stepIndex !== undefined)
      if (!parentRequest)
        throw new Error('The native child sequence contains no parent model request.')
      expect(nativeModelToolNames(parentRequest).some(name => ['Workflow', 'SubagentWorkflow', 'workflow'].includes(name))).toBe(false)
    },
  })
})
