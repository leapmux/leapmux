import { kimiExtractControl } from '../../../src/components/chat/providers/kimi/extractControl'

import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { createNativePermissionFileWrite, exerciseNativePermissionDecision } from '../helpers/nativePermission'

import { chooseSettingsOption, waitForSettingsIdle } from '../helpers/ui'
import { exerciseUnsupportedNativeControl } from '../helpers/unsupportedNativeControl'
import { kimiTest } from '../kimi-fixtures'

// LeapMux exposes no native multiline editor route for this provider.
kimiTest('classifies real native controls and proves the missing editor-requests route', async ({ page, modelScript, leapmuxServer, authenticatedKimiWorkspace }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedKimiWorkspace.workspaceId, provider: AgentProvider.KIMI_CODE }
  await chooseSettingsOption(page, 'permissionMode-manual')
  await waitForSettingsIdle(page)
  const operation = await createNativePermissionFileWrite(context, { fileName: 'native-editor-control.txt', callId: 'native-editor-permission', outputPrefix: 'NATIVECONTROL' })
  await exerciseUnsupportedNativeControl(context, {
    purpose: 'editor',
    classify: kimiExtractControl,
    relatedProof: beforeDecision => exerciseNativePermissionDecision(context, {
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
