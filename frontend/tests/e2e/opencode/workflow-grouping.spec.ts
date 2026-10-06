import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { nativeModelToolNames } from '../helpers/nativeScenario'
import { spawnSubagentToolCall } from '../helpers/providerToolCalls'
import { openRunningNativeChild } from '../helpers/runningChildProof'
import { exerciseUngroupedNativeChildren } from '../helpers/ungroupedNativeChildren'
import { opencodeTest } from '../opencode-fixtures'

opencodeTest('keeps two actual native children outside workflow groups after reload', async ({ authenticatedOpencodeWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedOpencodeWorkspace.workspaceId, provider: AgentProvider.OPENCODE }
  const suffix = crypto.randomUUID().replaceAll('-', '')
  await exerciseUngroupedNativeChildren(context, {
    openChild: async (index) => {
      const gate = `group-child-${index}-${suffix}`
      const description = `Actual grouping child ${index} ${suffix}`
      return openRunningNativeChild(context, {
        spawn: spawnSubagentToolCall(AgentProvider.OPENCODE, `native-group-child-${index}`, { description, prompt: modelScript.prompt(`NATIVE_GROUP_CHILD_${index}_${suffix}: reply once.`) }),
        gate,
        childMatcher: { user: `^NATIVE_GROUP_CHILD_${index}_${suffix}` },
        childFinalStep: { text: `ACTUAL_CHILD_REPORT_${index}_${suffix}` },
        allowExistingRows: index > 0,
        rowText: description,
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
