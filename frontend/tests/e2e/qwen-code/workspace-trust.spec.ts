import { mkdirSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { qwenExtractControl } from '../../../src/components/chat/providers/qwen/extractControl'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { createNativePermissionFileWrite, exerciseNativePermissionDecision } from '../helpers/nativePermission'

import { nativeModelInstructionText } from '../helpers/nativeScenario'
import { exerciseNativeWorkspaceTrustLimit } from '../helpers/nativeWorkspaceTrustLimit'

import { chooseSettingsOption, waitForSettingsIdle } from '../helpers/ui'
import { exerciseUnsupportedNativeControl } from '../helpers/unsupportedNativeControl'
import { qwenTest } from '../qwen-fixtures'

// LeapMux exposes no interactive native workspace-trust route for this provider.
qwenTest('classifies real native controls and proves the missing workspace-trust route', async ({ page, modelScript, leapmuxServer, authenticatedQwenWorkspace }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedQwenWorkspace.workspaceId, provider: AgentProvider.QWEN_CODE }
  await chooseSettingsOption(page, 'permissionMode-default')
  await waitForSettingsIdle(page)
  const operation = await createNativePermissionFileWrite(context, { fileName: 'native-workspace-trust-control.txt', callId: 'native-workspace-trust-permission', outputPrefix: 'NATIVECONTROL' })
  await exerciseUnsupportedNativeControl(context, {
    purpose: 'workspace-trust',
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

qwenTest('loads project context configuration without a workspace trust decision', async ({ page, modelScript, leapmuxServer, authenticatedQwenWorkspace }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedQwenWorkspace.workspaceId, provider: AgentProvider.QWEN_CODE }
  await exerciseNativeWorkspaceTrustLimit(context, {
    projectConfiguration: {
      prepare: ({ directory, marker }) => {
        const filename = `context-${marker}.md`
        mkdirSync(join(directory, '.qwen'), { recursive: true })
        writeFileSync(join(directory, '.qwen', 'settings.json'), JSON.stringify({ context: { fileName: filename } }))
        writeFileSync(join(directory, filename), `Project context: ${marker}.\n`)
      },
      prove: async (privateContext, { marker }) => {
        const request = await sendNativeAnswer(privateContext, 'Reply once under the project context configuration.', 'The project context turn completed.')
        expect(nativeModelInstructionText(request)).toContain(marker)
      },
    },
  })
})
