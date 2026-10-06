import { qwenExtractControl } from '../../../src/components/chat/providers/qwen/extractControl'

import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { createNativePermissionFileWrite, exerciseNativePermissionDecision } from '../helpers/nativePermission'

import { chooseSettingsOption, waitForSettingsIdle } from '../helpers/ui'
import { exerciseUnsupportedNativeControl } from '../helpers/unsupportedNativeControl'
import { qwenTest } from '../qwen-fixtures'

// LeapMux exposes no native multiline editor route for this provider.
qwenTest('classifies real native controls and proves the missing editor-requests route', async ({ page, modelScript, leapmuxServer, authenticatedQwenWorkspace }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedQwenWorkspace.workspaceId, provider: AgentProvider.QWEN_CODE }
  await chooseSettingsOption(page, 'permissionMode-default')
  await waitForSettingsIdle(page)
  const operation = await createNativePermissionFileWrite(context, { fileName: 'native-editor-control.txt', callId: 'native-editor-permission', outputPrefix: 'NATIVECONTROL' })
  await exerciseUnsupportedNativeControl(context, {
    purpose: 'editor',
    classify: qwenExtractControl,
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
