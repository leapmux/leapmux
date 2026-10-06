import { existsSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { ohMyPiExtractControl } from '../../../src/components/chat/providers/ohmypi/extractControl'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { createNativePermissionFileWrite, exerciseNativePermissionDecision } from '../helpers/nativePermission'

import { exerciseNativeWorkspaceTrustLimit } from '../helpers/nativeWorkspaceTrustLimit'

import { chooseSettingsOption, waitForSettingsIdle } from '../helpers/ui'
import { exerciseUnsupportedNativeControl } from '../helpers/unsupportedNativeControl'
import { ohMyPiTest } from '../ohmypi-fixtures'

// LeapMux exposes no interactive native workspace-trust route for this provider.
ohMyPiTest('classifies real native controls and proves the missing workspace-trust route', async ({ page, modelScript, leapmuxServer, authenticatedOhMyPiWorkspace }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedOhMyPiWorkspace.workspaceId, provider: AgentProvider.OH_MY_PI }
  await chooseSettingsOption(page, 'permissionMode-always-ask')
  await waitForSettingsIdle(page)
  const operation = await createNativePermissionFileWrite(context, { fileName: 'native-workspace-trust-control.txt', callId: 'native-workspace-trust-permission', outputPrefix: 'NATIVECONTROL' })
  await exerciseUnsupportedNativeControl(context, {
    purpose: 'workspace-trust',
    classify: ohMyPiExtractControl,
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

ohMyPiTest('loads an executable project extension without a workspace trust decision', async ({ page, modelScript, leapmuxServer, authenticatedOhMyPiWorkspace }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedOhMyPiWorkspace.workspaceId, provider: AgentProvider.OH_MY_PI }
  await exerciseNativeWorkspaceTrustLimit(context, {
    projectConfiguration: {
      prepare: ({ directory, marker }) => {
        const extensions = join(directory, '.omp', 'extensions')
        mkdirSync(extensions, { recursive: true })
        writeFileSync(join(extensions, 'e2e-project-config.js'), `import { writeFileSync } from 'node:fs';\nexport default function() { writeFileSync(${JSON.stringify(join(directory, 'native-project-extension.txt'))}, ${JSON.stringify(marker)}) }\n`)
      },
      prove: async (privateContext, { directory, marker }) => {
        const receipt = join(directory, 'native-project-extension.txt')
        await expect.poll(() => existsSync(receipt)).toBe(true)
        expect(readFileSync(receipt, 'utf8')).toBe(marker)
        await sendNativeAnswer(privateContext, 'Reply once after native project extension initialization.', 'The project extension turn completed.')
        expect(readFileSync(receipt, 'utf8')).toBe(marker)
      },
    },
  })
})
