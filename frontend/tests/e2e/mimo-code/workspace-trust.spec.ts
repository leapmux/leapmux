import { writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { mimoExtractControl } from '../../../src/components/chat/providers/mimo/extractControl'
import { MIMO_OPTION, MIMO_PERMISSION_POLICY } from '../../../src/generated/contracts/mimo-protocol'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { nativeModelInstructionText } from '../helpers/nativeScenario'
import { exerciseMissingWorkspaceTrustRoute, exerciseNativeWorkspaceTrustLimit, projectConfigurationWorker } from '../helpers/nativeWorkspaceTrustLimit'
import { mimoTest } from '../mimo-fixtures'
import { createMiMoControlDeletion } from './controlScenarios'
import { nativeLaunch } from './scenarios'

mimoTest('classifies real native controls and proves the missing workspace-trust route', async ({ native }) => {
  await exerciseMissingWorkspaceTrustRoute(native, {
    askOption: `${MIMO_OPTION.PermissionPolicy}-${MIMO_PERMISSION_POLICY.Ask}`,
    classify: mimoExtractControl,
    operation: () => createMiMoControlDeletion(native, 'workspace-trust'),
  })
})

mimoTest('loads project instructions without a workspace trust decision', async ({ native, leapmuxServer }) => {
  await exerciseNativeWorkspaceTrustLimit(native, {
    worker: projectConfigurationWorker(leapmuxServer.agentEnv, nativeLaunch(native), 'MIMOCODE_DISABLE_PROJECT_CONFIG'),
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
