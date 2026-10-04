import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { codexExtractControl } from '../../../src/components/chat/providers/codex/extractControl'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { codexTest } from '../codex-fixtures'
import { exerciseNativePermissionDecision } from '../helpers/nativePermission'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { codexEscalatedCommandToolCall } from '../helpers/providerToolCalls'
import { createTestDirectory } from '../helpers/runDirectory'
import { quotePosixShellArgument } from '../helpers/shellArguments'
import { chooseSettingsOption, waitForSettingsIdle } from '../helpers/ui'
import { exerciseUnsupportedNativeControl } from '../helpers/unsupportedNativeControl'

codexTest('classifies an actual native permission and confirms the absent editor-requests route', async ({ authenticatedCodexWorkspace, page, leapmuxServer, modelScript }) => {
  const context = { page, modelScript, leapmuxServer, provider: AgentProvider.CODEX, workspaceId: authenticatedCodexWorkspace.workspaceId }
  await chooseSettingsOption(page, 'permissionMode-on-request')
  await waitForSettingsIdle(page)
  const file = join(createTestDirectory('native-editor-'), 'native-control.txt')
  const callId = 'native-control-permission'
  const command = `printf 'NATIVECONTROL%s\\n' "$((40 + 2))" > ${quotePosixShellArgument(file)}; cat ${quotePosixShellArgument(file)}`
  await exerciseUnsupportedNativeControl(context, {
    purpose: 'editor',
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
