import { mimoExtractControl } from '../../../src/components/chat/providers/mimo/extractControl'
import { MIMO_OPTION, MIMO_PERMISSION_POLICY } from '../../../src/generated/contracts/mimo-protocol'

import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseNativePermissionDecision } from '../helpers/nativePermission'
import { chooseSettingsOption, waitForSettingsIdle } from '../helpers/ui'

import { exerciseUnsupportedNativeControl } from '../helpers/unsupportedNativeControl'
import { mimoTest } from '../mimo-fixtures'
import { createMiMoControlDeletion } from './controlScenarios'

// LeapMux exposes no native multiline editor route for this provider.
mimoTest('classifies real native controls and proves the missing editor-requests route', async ({ page, modelScript, leapmuxServer, authenticatedMiMoWorkspace }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedMiMoWorkspace.workspaceId, provider: AgentProvider.MIMO_CODE }
  await chooseSettingsOption(page, `${MIMO_OPTION.PermissionPolicy}-${MIMO_PERMISSION_POLICY.Ask}`)
  await waitForSettingsIdle(page)
  const operation = await createMiMoControlDeletion(context, 'editor')
  await exerciseUnsupportedNativeControl(context, {
    purpose: 'editor',
    classify: mimoExtractControl,
    nativeOperation: beforeDecision => exerciseNativePermissionDecision(context, {
      toolCall: operation.toolCall,
      outputGate: operation.outputGate,
      decision: 'allow',
      beforeDecision: async (banner) => {
        await operation.beforeDecision()
        await beforeDecision(banner)
      },
      nativeProof: operation.nativeProof,
    }),
  })
})
