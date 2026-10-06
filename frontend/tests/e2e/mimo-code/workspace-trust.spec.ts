import { writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { mimoExtractControl } from '../../../src/components/chat/providers/mimo/extractControl'
import { MIMO_OPTION, MIMO_PERMISSION_POLICY } from '../../../src/generated/contracts/mimo-protocol'
import { exerciseMissingWorkspaceTrustRoute, exerciseNativeWorkspaceTrustLimit, instructionFileConfiguration, projectConfigurationWorker } from '../helpers/nativeWorkspaceTrustLimit'
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

/** The instruction file of the project. MiMo Code reads no file of this name unless its project configuration lists it. */
const PROJECT_INSTRUCTIONS = 'native-project-instructions.md'

mimoTest('loads project instructions without a workspace trust decision', async ({ native, leapmuxServer }) => {
  const instructions = instructionFileConfiguration(PROJECT_INSTRUCTIONS)
  await exerciseNativeWorkspaceTrustLimit(native, {
    worker: projectConfigurationWorker(leapmuxServer.agentEnv, nativeLaunch(native), 'MIMOCODE_DISABLE_PROJECT_CONFIG'),
    projectConfiguration: {
      prepare: (project) => {
        instructions.prepare(project)
        writeFileSync(join(project.directory, 'mimocode.json'), JSON.stringify({ instructions: [join(project.directory, PROJECT_INSTRUCTIONS)] }))
      },
      prove: instructions.prove,
    },
  })
})
