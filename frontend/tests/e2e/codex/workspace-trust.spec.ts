import { existsSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { expect } from '@playwright/test'
import { codexExtractControl } from '../../../src/components/chat/providers/codex/extractControl'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { codexTest } from '../codex-fixtures'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { exerciseNativePermissionDecision } from '../helpers/nativePermission'
import { nativeModelInstructionText } from '../helpers/nativeScenario'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { exerciseNativeWorkspaceTrustLimit } from '../helpers/nativeWorkspaceTrustLimit'
import { codexEscalatedCommandToolCall } from '../helpers/providerToolCalls'
import { createTestDirectory } from '../helpers/runDirectory'
import { quotePosixShellArgument } from '../helpers/shellArguments'
import { chooseSettingsOption, waitForSettingsIdle } from '../helpers/ui'
import { exerciseUnsupportedNativeControl } from '../helpers/unsupportedNativeControl'
import { createGitRepo } from '../helpers/worktree'

codexTest('classifies an actual native permission and confirms the absent workspace-trust route', async ({ authenticatedCodexWorkspace, page, leapmuxServer, modelScript }) => {
  const context = { page, modelScript, leapmuxServer, provider: AgentProvider.CODEX, workspaceId: authenticatedCodexWorkspace.workspaceId }
  await chooseSettingsOption(page, 'permissionMode-on-request')
  await waitForSettingsIdle(page)
  const file = join(createTestDirectory('native-workspace-trust-'), 'native-control.txt')
  const callId = 'native-control-permission'
  const command = `printf 'NATIVECONTROL%s\\n' "$((40 + 2))" > ${quotePosixShellArgument(file)}; cat ${quotePosixShellArgument(file)}`
  await exerciseUnsupportedNativeControl(context, {
    purpose: 'workspace-trust',
    classify: codexExtractControl,
    nativeOperation: beforeDecision => exerciseNativePermissionDecision(context, {
      toolCall: codexEscalatedCommandToolCall(callId, command),
      decision: 'allow',
      beforeDecision: async (banner) => {
        expect(existsSync(file)).toBe(false)
        await beforeDecision(banner)
      },
      nativeProof: (request) => {
        expect(nativeToolResult(request, callId)).toContain('NATIVECONTROL42')
        expect(readFileSync(file, 'utf8')).toBe('NATIVECONTROL42\n')
      },
    }),
  })
})

for (const sandboxPolicy of ['read-only', 'danger-full-access']) {
  codexTest(`processes actual project configuration without a trust request under ${sandboxPolicy}`, async ({ authenticatedCodexWorkspace, page, leapmuxServer, modelScript }) => {
    const context = { page, modelScript, leapmuxServer, provider: AgentProvider.CODEX, workspaceId: authenticatedCodexWorkspace.workspaceId }
    await exerciseNativeWorkspaceTrustLimit(context, {
      optionValues: { sandbox_policy: sandboxPolicy, permissionMode: 'never' },
      projectConfiguration: {
        prepare: ({ directory, marker }) => {
          // A separate repository keeps earlier parent-project trust decisions outside this project root.
          createGitRepo(directory, '.')
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
