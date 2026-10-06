import { mkdirSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { qwenExtractControl } from '../../../src/components/chat/providers/qwen/extractControl'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { nativeModelInstructionText } from '../helpers/nativeScenario'
import { exerciseMissingWorkspaceTrustRoute, exerciseNativeWorkspaceTrustLimit } from '../helpers/nativeWorkspaceTrustLimit'
import { qwenTest } from '../qwen-fixtures'

qwenTest('classifies real native controls and proves the missing workspace-trust route', async ({ native }) => {
  await exerciseMissingWorkspaceTrustRoute(native, { askOption: 'permissionMode-default', classify: qwenExtractControl })
})

qwenTest('loads project context configuration without a workspace trust decision', async ({ native }) => {
  await exerciseNativeWorkspaceTrustLimit(native, {
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
