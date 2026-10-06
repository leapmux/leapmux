import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { nativeModelToolNames } from '../helpers/nativeScenario'
import { spawnSubagentToolCall } from '../helpers/providerToolCalls'
import { openRunningNativeChild } from '../helpers/runningChildProof'
import { uniqueMarker } from '../helpers/shellArguments'
import { applyPermissionPreset } from '../helpers/ui'
import { exerciseUngroupedNativeChildren } from '../helpers/ungroupedNativeChildren'
import { reasonixTest } from '../reasonix-fixtures'
import { readReasonixChildTaskId, reasonixChildTaskMatcher } from './childIdentity'

reasonixTest('keeps two actual native children outside workflow groups after reload', async ({ authenticatedReasonixWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedReasonixWorkspace.workspaceId, provider: AgentProvider.REASONIX }
  const suffix = uniqueMarker()
  await exerciseUngroupedNativeChildren(context, {
    openChild: async (index) => {
      const gate = `group-child-${index}-${suffix}`
      const description = `Actual grouping child ${index} ${suffix}`
      const prompt = modelScript.prompt(`NATIVE_GROUP_CHILD_${index}_${suffix}: reply once.`)
      const spawn = spawnSubagentToolCall(AgentProvider.REASONIX, `native-group-child-${index}`, { description, prompt })
      return openRunningNativeChild(context, {
        spawn,
        resolveTaskId: parentId => readReasonixChildTaskId(context, parentId, spawn.id, prompt),
        gate,
        childMatcher: reasonixChildTaskMatcher(`NATIVE_GROUP_CHILD_${index}_${suffix}`),
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
