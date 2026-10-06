import { kiroExtractControl } from '../../../src/components/chat/providers/kiro/extractControl'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'

import { kiroToolResult } from '../helpers/kiroToolResult'
import { createNativePermissionFileWrite, exerciseNativePermissionDecision } from '../helpers/nativePermission'

import { chooseSettingsOption, waitForSettingsIdle } from '../helpers/ui'
import { exerciseUnsupportedNativeControl } from '../helpers/unsupportedNativeControl'
import { kiroTest } from '../kiro-fixtures'

// LeapMux exposes no native multiline editor route for this provider.
kiroTest('classifies real native controls and proves the missing editor-requests route', async ({ page, modelScript, leapmuxServer, authenticatedKiroWorkspace }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedKiroWorkspace.workspaceId, provider: AgentProvider.KIRO, readToolResult: kiroToolResult }
  await chooseSettingsOption(page, 'policyPreset-ask')
  await waitForSettingsIdle(page)
  const operation = await createNativePermissionFileWrite(context, { fileName: 'native-editor-control.txt', callId: 'native-editor-permission', outputPrefix: 'NATIVECONTROL' })
  await exerciseUnsupportedNativeControl(context, {
    purpose: 'editor',
    classify: kiroExtractControl,
    nativeOperation: beforeDecision => exerciseNativePermissionDecision(context, {
      toolCall: operation.toolCall,
      decision: 'allow',
      beforeDecision: async (banner) => {
        await operation.beforeDecision()
        await beforeDecision(banner)
      },
      nativeProof: operation.nativeProof,
    }),
  })
})
