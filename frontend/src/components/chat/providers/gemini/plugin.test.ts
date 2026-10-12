import type { ParsedMessageContent } from '~/lib/messageParser'
import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { providerToolCall } from '~/test-support/toolCallFixture'
import { acpTextContent, describeACPProviderBasics, renderACPToolPair } from '../acp/testUtils'
import { providerFor } from '../registry'
import { input } from '../testUtils'

import './plugin'

describe('gemini provider', () => {
  describeACPProviderBasics(AgentProvider.GEMINI_CLI, { text: true, image: true, pdf: true, binary: true })

  it('exposes native modes and a bypass permission preset', () => {
    const plugin = providerFor(AgentProvider.GEMINI_CLI)
    expect(plugin?.configuration?.triggerModeGroupKey).toBe('permissionMode')
    expect(plugin?.configuration?.planMode).toBeDefined()
    expect(plugin?.controls?.permissionPresets).toEqual({ bypass: { sets: { permissionMode: 'yolo' } } })
    expect(plugin?.configuration?.effortGroupKey).toBeUndefined()
  })
})

describe('native output without a filesystem pointer', () => {
  it('keeps a valid native result and omits the output path hook', () => {
    const frame = {
      sessionUpdate: 'tool_call_update',
      toolCallId: 'native-call',
      status: 'completed',
      kind: 'execute',
      title: 'native command',
      rawInput: {
        command: 'printf preview',
      },
      content: [
        {
          type: 'content',
          content: {
            type: 'text',
            text: 'native inline preview',
          },
        },
      ],
      rawOutput: {
        output: 'native inline preview',
        exitCode: 0,
      },
    }
    const call = providerToolCall(AgentProvider.GEMINI_CLI, frame, { spanId: 'native-call', spanType: 'run_shell_command', agentSessionId: 'native-session', role: 'result' })
    expect(call).not.toBeNull()
    expect(call?.id).toBe('native-call')
    expect(call?.outputFilePaths).toBeUndefined()
    const registered = providerFor(AgentProvider.GEMINI_CLI)
    expect(registered).toBeDefined()
    expect(registered?.transcript.outputFilePaths).toBeUndefined()
  })
})

/**
 * The Deny answer, as the two frames Gemini CLI sends for it: the pending call that asks
 * for permission, and the failed update that refuses it (`runTool`,
 * `packages/cli/src/acp/acpSession.ts`).
 */
describe('gemini refused tool calls', () => {
  const REFUSAL = 'Tool "run_shell_command" was canceled by the user.'
  const opening = { sessionUpdate: 'tool_call', toolCallId: 'run_shell_command__denied', status: 'pending', title: 'printf ready', kind: 'execute', content: [], locations: [] }
  const ending = { sessionUpdate: 'tool_call_update', toolCallId: 'run_shell_command__denied', status: 'failed', kind: 'execute', content: acpTextContent(REFUSAL) }

  it('reads the Deny answer as declined, with the refusal as the result', () => {
    const call = providerToolCall(AgentProvider.GEMINI_CLI, ending, { request: input(opening, null, AgentProvider.GEMINI_CLI) as ParsedMessageContent, spanType: 'tool_call_update' })
    expect(call?.kind).toBe('execute')
    expect(call?.status).toBe('declined')
    expect(call?.result).toStrictEqual({ failure: true, text: REFUSAL })
  })

  it('heads the refused command as declined rather than as an error', () => {
    const { container } = renderACPToolPair(
      AgentProvider.GEMINI_CLI,
      { title: 'printf ready', kind: 'execute', content: [], locations: [] },
      { status: 'failed', kind: 'execute', content: acpTextContent(REFUSAL) },
    )
    expect(container.textContent).toContain('Declined')
    expect(container.textContent).toContain(REFUSAL)
    expect(container.textContent).not.toContain('Error')
  })
})

/**
 * A refused file tool. Gemini CLI sends the edit kind for both `write_file` and
 * `replace`. The opening call carries the PROPOSED diff and lists the file under
 * `locations`, and it states no raw input. The failed update replaces the content with
 * the refusal. The joined call therefore states the file in `locations` alone.
 */
describe('gemini refused file tools', () => {
  const proposed = { type: 'diff', path: '/w/notes.txt', oldText: 'proposedBefore\n', newText: 'proposedAfter\n', _meta: { kind: 'modify' } }
  const opening = (toolCallId: string) => ({ sessionUpdate: 'tool_call', toolCallId, status: 'pending', title: 'Writing to notes.txt', kind: 'edit', content: [proposed], locations: [{ path: '/w/notes.txt' }] })
  const ending = (toolCallId: string, refusal: string) => ({ sessionUpdate: 'tool_call_update', toolCallId, status: 'failed', kind: 'edit', content: acpTextContent(refusal) })

  it.each([
    ['write_file', 'write', 'add'],
    ['replace', 'edit', 'edit'],
  ] as const)('keeps the file of a refused %s, with the refusal as the result', (toolName, kind, operation) => {
    const refusal = `Tool "${toolName}" was canceled by the user.`
    const toolCallId = `${toolName}__denied`
    const call = providerToolCall(AgentProvider.GEMINI_CLI, ending(toolCallId, refusal), { request: input(opening(toolCallId), null, AgentProvider.GEMINI_CLI) as ParsedMessageContent, spanType: 'tool_call_update' })
    expect(call?.degradation).toBeUndefined()
    expect(call?.kind).toBe(kind)
    expect(call?.status).toBe('declined')
    expect(call?.kind === 'edit' || call?.kind === 'write' ? call.request.changes : []).toStrictEqual([
      { filePath: '/w/notes.txt', operation, oldStr: '', newStr: '', structuredPatch: null, showLineNumbers: false },
    ])
    expect(call?.result).toStrictEqual({ failure: true, text: refusal })
  })

  it('heads a refused write with its file and draws no proposed diff', () => {
    const refusal = 'Tool "write_file" was canceled by the user.'
    const { container } = renderACPToolPair(AgentProvider.GEMINI_CLI, opening('write_file__denied'), ending('write_file__denied', refusal))
    expect(container.textContent).toContain('Declined')
    expect(container.textContent).toContain(refusal)
    expect(container.textContent).toContain('/w/notes.txt')
    expect(container.textContent).not.toContain('proposedAfter')
    expect(container.textContent).not.toContain('Error')
  })
})
