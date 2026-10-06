import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { codexExtractControl } from '../../../src/components/chat/providers/codex/extractControl'
import { codexTest } from '../codex-fixtures'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { codexEscalatedCommandToolCall } from '../helpers/providerToolCalls'
import { createTestDirectory } from '../helpers/runDirectory'
import { quotePosixShellArgument } from '../helpers/shellArguments'
import { chooseSettingsOption, waitForSettingsIdle } from '../helpers/ui'
import { exerciseUnsupportedControlThroughPermission } from '../helpers/unsupportedNativeControl'

// The escalated command is the tool that requests Codex's native approval (see `codexEscalatedCommandToolCall`), so
// the proof keeps it and does not use the default write.
codexTest('classifies an actual native permission and confirms the absent editor-requests route', async ({ native }) => {
  await chooseSettingsOption(native.page, 'permissionMode-on-request')
  await waitForSettingsIdle(native.page)
  const file = join(createTestDirectory('native-editor-'), 'native-control.txt')
  const callId = 'native-control-permission'
  const command = `printf 'NATIVECONTROL%s\\n' "$((40 + 2))" > ${quotePosixShellArgument(file)}; cat ${quotePosixShellArgument(file)}`
  await exerciseUnsupportedControlThroughPermission(native, {
    purpose: 'editor',
    classify: codexExtractControl,
    operation: {
      toolCall: codexEscalatedCommandToolCall(callId, command),
      beforeDecision: () => expect(existsSync(file)).toBe(false),
      nativeProof: (request) => {
        expect(nativeToolResult(request, callId)).toContain('NATIVECONTROL42')
        expect(readFileSync(file, 'utf8')).toBe('NATIVECONTROL42\n')
      },
    },
  })
})
