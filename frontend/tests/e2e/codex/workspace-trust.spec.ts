import { mkdirSync, writeFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { expect } from '@playwright/test'
import { codexExtractControl } from '../../../src/components/chat/providers/codex/extractControl'
import { CODEX_OPTION } from '../../../src/generated/contracts/codex-protocol'
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

// Codex trusts a project by itself when the effective permission profile can write the working directory, and only
// then reads the project's own configuration. The read-only profile cannot, and the full-access profile can.
for (const permissionProfile of [':read-only', ':danger-full-access']) {
  codexTest(`processes actual project configuration without a trust request under ${permissionProfile}`, async ({ native }) => {
    await exerciseNativeWorkspaceTrustLimit(native, {
      optionValues: { [CODEX_OPTION.PermissionProfile]: permissionProfile, permissionMode: 'never' },
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
          if (permissionProfile === ':read-only')
            expect(instructions).not.toContain(marker)
          else
            expect(instructions).toContain(marker)
        },
      },
    })
  })
}
