import { writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { mimoExtractControl } from '../../../src/components/chat/providers/mimo/extractControl'
import { MIMO_OPTION, MIMO_PERMISSION_POLICY } from '../../../src/generated/contracts/mimo-protocol'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { findBinary } from '../helpers/binaryOnPath'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { exerciseNativePermissionDecision } from '../helpers/nativePermission'
import { nativeModelInstructionText } from '../helpers/nativeScenario'
import { exerciseNativeWorkspaceTrustLimit } from '../helpers/nativeWorkspaceTrustLimit'
import { hubSpawnEnv } from '../helpers/server'

import { chooseSettingsOption, waitForSettingsIdle } from '../helpers/ui'

import { exerciseUnsupportedNativeControl } from '../helpers/unsupportedNativeControl'
import { MIMO_E2E_SKIP_REASON, mimoTest } from '../mimo-fixtures'
import { createMiMoControlDeletion } from './controlScenarios'

mimoTest.skip(!!MIMO_E2E_SKIP_REASON, MIMO_E2E_SKIP_REASON || '')

// LeapMux exposes no interactive native workspace-trust route for this provider.
mimoTest('classifies real native controls and proves the missing workspace-trust route', async ({ page, modelScript, leapmuxServer, authenticatedMiMoWorkspace }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedMiMoWorkspace.workspaceId, provider: AgentProvider.MIMO_CODE }
  await chooseSettingsOption(page, `${MIMO_OPTION.PermissionPolicy}-${MIMO_PERMISSION_POLICY.Ask}`)
  await waitForSettingsIdle(page)
  const operation = await createMiMoControlDeletion(context, 'workspace-trust')
  await exerciseUnsupportedNativeControl(context, {
    purpose: 'workspace-trust',
    classify: mimoExtractControl,
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

mimoTest('loads project instructions without a workspace trust decision', async ({ page, modelScript, leapmuxServer, authenticatedMiMoWorkspace }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedMiMoWorkspace.workspaceId, provider: AgentProvider.MIMO_CODE }
  const executable = findBinary('mimo', hubSpawnEnv(leapmuxServer.agentEnv))
  if (!executable)
    throw new Error('The installed MiMo executable is absent.')
  await exerciseNativeWorkspaceTrustLimit(context, {
    worker: { launch: { binaryName: 'mimo', executable, holdWhen: ['serve'], lazy: false }, workerEnvironment: () => ({ MIMOCODE_DISABLE_PROJECT_CONFIG: 'false' }) },
    projectConfiguration: {
      prepare: ({ directory, marker }) => {
        const instructions = join(directory, 'native-project-instructions.md')
        writeFileSync(instructions, `Project instruction: ${marker}.\n`)
        writeFileSync(join(directory, 'mimocode.json'), JSON.stringify({ instructions: [instructions] }))
      },
      prove: async (privateContext, { marker }) => {
        const request = await sendNativeAnswer(privateContext, 'Reply once under the project instructions.', 'The project instruction turn completed.')
        expect(nativeModelInstructionText(request)).toContain(marker)
      },
    },
  })
})
