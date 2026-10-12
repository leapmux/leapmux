import { existsSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import process from 'node:process'
import { expect } from '@playwright/test'
import { writeNativeProjectHook } from '../helpers/nativeProjectHook'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { exerciseNativeWorkspaceTrustLimit } from '../helpers/nativeWorkspaceTrustLimit'
import { readToolCall } from '../helpers/providerToolCalls'
import { quotePosixShellArgument } from '../helpers/shellArguments'
import { sendMessage, waitForAgentIdle } from '../helpers/ui'
import { lettaTest } from '../letta-fixtures'
import { nativeContext } from './scenarios'

lettaTest('starts with a real project configuration and no native workspace trust barrier', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
  const callId = 'letta-workspace-config-read'
  await exerciseNativeWorkspaceTrustLimit(context, {
    projectConfiguration: {
      prepare: ({ directory, marker }) => {
        const hook = writeNativeProjectHook(directory, marker)
        const configuration = join(directory, '.letta', 'settings.json')
        mkdirSync(dirname(configuration), { recursive: true })
        // Letta reads project hooks from this settings file. It has no project .mcp.json loader.
        writeFileSync(configuration, JSON.stringify({ hooks: { PreToolUse: [{ matcher: 'Read', hooks: [{ type: 'command', command: `${quotePosixShellArgument(process.execPath)} ${quotePosixShellArgument(hook.scriptPath)}` }] }] } }))
        writeFileSync(join(directory, 'native-read.txt'), `${marker}\n`)
      },
      prove: async (privateContext, { directory, marker }) => {
        const start = await modelScript.queue(
          { toolCalls: [readToolCall(privateContext.provider, callId, join(directory, 'native-read.txt'))] },
          { text: 'The native project configuration probe completed.' },
        )
        await sendMessage(page, modelScript.prompt('Read the private file once and then complete.'))
        await modelScript.waitForSteps(start + 2)
        await waitForAgentIdle(page)
        expect(nativeToolResult(await modelScript.requestAt(start + 1), callId)).toContain(marker)
        const receipt = join(directory, 'native-project-hook-receipt.json')
        expect(existsSync(receipt)).toBe(true)
        const actual: unknown = JSON.parse(readFileSync(receipt, 'utf8'))
        expect(actual).toMatchObject({ marker, workingDirectory: directory, input: { event_type: 'PreToolUse', working_directory: directory, tool_name: 'Read', tool_call_id: callId } })
      },
    },
  })
})
