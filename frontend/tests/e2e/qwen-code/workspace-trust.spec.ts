import { mkdirSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { qwenExtractControl } from '../../../src/components/chat/providers/qwen/extractControl'
import { exerciseMissingWorkspaceTrustRoute, exerciseNativeWorkspaceTrustLimit, instructionFileConfiguration } from '../helpers/nativeWorkspaceTrustLimit'
import { qwenTest } from '../qwen-fixtures'

qwenTest('classifies real native controls and proves the missing workspace-trust route', async ({ native }) => {
  await exerciseMissingWorkspaceTrustRoute(native, { askOption: 'permissionMode-default', classify: qwenExtractControl })
})

/** The context file of the project. Qwen Code reads no file of this name unless its project settings give the name. */
const PROJECT_CONTEXT = 'native-project-context.md'

qwenTest('loads project context configuration without a workspace trust decision', async ({ native }) => {
  const context = instructionFileConfiguration(PROJECT_CONTEXT)
  await exerciseNativeWorkspaceTrustLimit(native, {
    projectConfiguration: {
      prepare: (project) => {
        context.prepare(project)
        mkdirSync(join(project.directory, '.qwen'), { recursive: true })
        writeFileSync(join(project.directory, '.qwen', 'settings.json'), JSON.stringify({ context: { fileName: PROJECT_CONTEXT } }))
      },
      prove: context.prove,
    },
  })
})
