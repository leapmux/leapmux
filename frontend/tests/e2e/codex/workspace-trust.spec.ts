import { mkdirSync, writeFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { expect } from '@playwright/test'
import { codexExtractControl } from '../../../src/components/chat/providers/codex/extractControl'
import { codexTest } from '../codex-fixtures'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { nativeModelInstructionText } from '../helpers/nativeScenario'
import { exerciseMissingWorkspaceTrustRoute, exerciseNativeWorkspaceTrustLimit, outsideFileWriteOperation } from '../helpers/nativeWorkspaceTrustLimit'
import { codexEscalatedCommandToolCall } from '../helpers/providerToolCalls'
import { ensureGitRepositoryRoot } from '../helpers/worktree'

codexTest('classifies an actual native permission and confirms the absent workspace-trust route', async ({ native }) => {
  await exerciseMissingWorkspaceTrustRoute(native, {
    askOption: 'permissionMode-on-request',
    classify: codexExtractControl,
    operation: () => outsideFileWriteOperation(codexEscalatedCommandToolCall),
  })
})

for (const sandboxPolicy of ['read-only', 'danger-full-access']) {
  codexTest(`processes actual project configuration without a trust request under ${sandboxPolicy}`, async ({ native }) => {
    await exerciseNativeWorkspaceTrustLimit(native, {
      optionValues: { sandbox_policy: sandboxPolicy, permissionMode: 'never' },
      projectConfiguration: {
        prepare: ({ directory, marker }) => {
          // A separate repository keeps earlier parent-project trust decisions outside this project root.
          ensureGitRepositoryRoot(directory)
          const configuration = join(directory, '.codex', 'config.toml')
          mkdirSync(dirname(configuration), { recursive: true })
          writeFileSync(configuration, `developer_instructions = ${JSON.stringify(marker)}\n`)
        },
        prove: async (privateContext, { marker }) => {
          const request = await sendNativeAnswer(privateContext, 'Reply once from this project.', 'The native project configuration turn completed.')
          expect(request.protocol).toBe('openai-responses')
          const instructions = nativeModelInstructionText(request)
          if (sandboxPolicy === 'read-only')
            expect(instructions).not.toContain(marker)
          else
            expect(instructions).toContain(marker)
        },
      },
    })
  })
}
