import { existsSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import process from 'node:process'
import { expect } from '@playwright/test'
import { claudeExtractControl } from '../../../src/components/chat/providers/claude/extractControl'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { claudeTest } from '../claude-fixtures'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { exerciseNativePermissionDecision } from '../helpers/nativePermission'
import { writeNativeProjectHook } from '../helpers/nativeProjectHook'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { exerciseNativeWorkspaceTrustLimit } from '../helpers/nativeWorkspaceTrustLimit'
import { bashToolCall } from '../helpers/providerToolCalls'
import { createTestDirectory } from '../helpers/runDirectory'
import { quotePosixShellArgument } from '../helpers/shellArguments'
import { chooseSettingsOption, waitForSettingsIdle } from '../helpers/ui'
import { exerciseUnsupportedNativeControl } from '../helpers/unsupportedNativeControl'

claudeTest('classifies an actual native permission and confirms the absent workspace-trust route', async ({ authenticatedClaudeWorkspace, page, leapmuxServer, modelScript }) => {
  const context = { page, modelScript, leapmuxServer, provider: AgentProvider.CLAUDE_CODE, workspaceId: authenticatedClaudeWorkspace.workspaceId }
  await chooseSettingsOption(page, 'permissionMode-default')
  await waitForSettingsIdle(page)
  const file = join(createTestDirectory('native-workspace-trust-'), 'native-control.txt')
  const callId = 'native-control-permission'
  const command = `printf 'NATIVECONTROL%s\\n' "$((40 + 2))" > ${quotePosixShellArgument(file)}; cat ${quotePosixShellArgument(file)}`
  await exerciseUnsupportedNativeControl(context, {
    purpose: 'workspace-trust',
    classify: claudeExtractControl,
    nativeOperation: beforeDecision => exerciseNativePermissionDecision(context, {
      toolCall: bashToolCall(AgentProvider.CLAUDE_CODE, callId, command),
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

claudeTest('loads the actual project startup hook without a native trust barrier', async ({ authenticatedClaudeWorkspace, page, leapmuxServer, modelScript }) => {
  const context = { page, modelScript, leapmuxServer, provider: AgentProvider.CLAUDE_CODE, workspaceId: authenticatedClaudeWorkspace.workspaceId }
  await exerciseNativeWorkspaceTrustLimit(context, {
    projectConfiguration: {
      prepare: ({ directory, marker }) => {
        const hook = writeNativeProjectHook(directory, marker)
        const configuration = join(directory, '.claude', 'settings.json')
        mkdirSync(dirname(configuration), { recursive: true })
        writeFileSync(configuration, JSON.stringify({ hooks: { SessionStart: [{ hooks: [{ type: 'command', command: `${quotePosixShellArgument(process.execPath)} ${quotePosixShellArgument(hook.scriptPath)}` }] }] } }))
      },
      prove: async (privateContext, { directory, marker }) => {
        await sendNativeAnswer(privateContext, 'Reply once from this project.', 'The native project startup turn completed.')
        const receipt = join(directory, 'native-project-hook-receipt.json')
        expect(existsSync(receipt)).toBe(true)
        const actual: unknown = JSON.parse(readFileSync(receipt, 'utf8'))
        expect(actual).toMatchObject({ marker, workingDirectory: directory, input: { hook_event_name: 'SessionStart', cwd: directory } })
      },
    },
  })
})
