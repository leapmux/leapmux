import { existsSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { ohMyPiExtractControl } from '../../../src/components/chat/providers/ohmypi/extractControl'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { exerciseMissingWorkspaceTrustRoute, exerciseNativeWorkspaceTrustLimit } from '../helpers/nativeWorkspaceTrustLimit'
import { ohMyPiTest } from '../ohmypi-fixtures'

ohMyPiTest('classifies real native controls and proves the missing workspace-trust route', async ({ native }) => {
  await exerciseMissingWorkspaceTrustRoute(native, { askOption: 'permissionMode-always-ask', classify: ohMyPiExtractControl })
})

ohMyPiTest('loads an executable project extension without a workspace trust decision', async ({ native }) => {
  await exerciseNativeWorkspaceTrustLimit(native, {
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
