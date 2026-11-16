import type { ACPToolCallAdapter } from './toolCall'
import { describe, expect, it, vi } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { isObject, pickString } from '~/lib/jsonPick'
import { resolveMessageForRendering } from '../../registry'
import { input } from '../../testUtils'
import { acpNeedsRequest } from '../spanRole'
import { acpExtractRow } from './row'

describe('acpExtractRow', () => {
  it.each(['request', 'result'] as const)('refuses an explicit no-side context before extracting a native %s', (side) => {
    const resolved = input({
      sessionUpdate: side === 'request'
        ? 'tool_call'
        : 'tool_call_update',
      toolCallId: 'native-role-call',
      kind: 'execute',
      status: side === 'request'
        ? 'pending'
        : 'completed',
      rawInput: { command: 'printf native' },
      ...(side === 'result'
        ? { rawOutput: 'Native bytes.' }
        : {}),
    }, null, AgentProvider.OPENCODE)
    const adapter = vi.fn<ACPToolCallAdapter>((_facts, base) => base())
    const span = {
      request: undefined,
      visibleRows: { request: false },
    }
    expect(acpExtractRow({
      resolved,
      category: { kind: 'tool_use' },
      span: { ...span, role: 'none' },
    }, adapter)).toBeNull()
    expect(adapter).not.toHaveBeenCalled()
    expect(acpExtractRow({
      resolved,
      category: { kind: 'tool_use' },
      span: { ...span, role: side },
    }, adapter)).toMatchObject({
      kind: 'tool',
      role: side,
      call: { id: 'native-role-call' },
    })
    expect(adapter).toHaveBeenCalledTimes(1)
  })

  it('keeps an independent plan row before the native-tool no-side refusal', () => {
    const resolved = input({
      sessionUpdate: 'plan',
      entries: [
        { content: 'Check the native plan.', status: 'pending', priority: 'medium' },
      ],
    }, null, AgentProvider.OPENCODE)
    const row = acpExtractRow({
      resolved,
      category: { kind: 'tool_use' },
      span: {
        request: undefined,
        role: 'none',
        visibleRows: { request: false },
      },
    })
    expect(row).toMatchObject({
      kind: 'tool',
      role: 'result',
      call: {
        kind: 'todo',
        request: {
          items: [
            { content: 'Check the native plan.' },
          ],
        },
      },
    })
  })

  it.each([undefined, '', 'stored-native-session'])('passes the stored row session to the adapter without trusting native input: %j', (session) => {
    const tool = {
      sessionUpdate: 'tool_call_update',
      toolCallId: 'native-call',
      kind: 'execute',
      title: 'printf native',
      status: 'completed',
      rawInput: { command: 'printf native' },
      rawOutput: 'native',
      agentSessionId: 'untrusted-protocol-session',
    }
    const parsed = resolveMessageForRendering({
      ...input(tool, undefined, AgentProvider.OPENCODE),
      ...(session === undefined
        ? {}
        : { agentSessionId: session }),
    }, AgentProvider.OPENCODE)
    let observed: unknown
    const adapter: ACPToolCallAdapter = (facts, base) => {
      observed = facts
      return base()
    }
    const row = acpExtractRow({
      resolved: parsed,
      category: { kind: 'tool_use' },
      span: {
        request: undefined,
        role: 'result',
        visibleRows: { request: false },
      },
    }, adapter)
    expect(row?.kind).toBe('tool')
    expect(isObject(observed)).toBe(true)
    if (!isObject(observed))
      throw new Error('The ACP adapter must receive its tool facts.')
    expect(pickString(observed, 'agentSessionId', undefined)).toBe(session)
  })

  // A row reads its own frame and the EARLIER request of its span, never its result.
  // These frames are one read: the request states the arguments, and the update that
  // closes the call states the output and no kind, title or arguments.
  describe('the two rows of one call', () => {
    const request = input({
      sessionUpdate: 'tool_call',
      toolCallId: 'pair',
      kind: 'read',
      title: 'Read README.md',
      status: 'pending',
      rawInput: { path: '/project/README.md' },
    }, null, AgentProvider.OPENCODE)
    const result = input({
      sessionUpdate: 'tool_call_update',
      toolCallId: 'pair',
      status: 'completed',
      content: [
        {
          type: 'content',
          content: { type: 'text', text: '1\tFile content' },
        },
      ],
    }, null, AgentProvider.OPENCODE)

    it('builds the request row from its own frame, with no result and no finished status', () => {
      const row = acpExtractRow({
        resolved: request,
        category: { kind: 'tool_use' },
        span: {
          request,
          role: 'request',
          visibleRows: { request: true },
        },
      })
      expect(row).toMatchObject({
        kind: 'tool',
        role: 'request',
        call: {
          kind: 'read',
          title: 'Read README.md',
          status: 'pending',
          request: { path: '/project/README.md' },
        },
      })
      expect(row?.kind === 'tool' && row.call.result).toBeUndefined()
    })

    it('builds the result row from its own frame plus the request', () => {
      const row = acpExtractRow({
        resolved: result,
        category: { kind: 'tool_use' },
        span: {
          request,
          role: 'result',
          visibleRows: { request: true },
        },
      })
      expect(row).toMatchObject({
        kind: 'tool',
        role: 'result',
        hasRequestRow: true,
        call: {
          kind: 'read',
          title: 'Read README.md',
          status: 'completed',
          request: { path: '/project/README.md' },
          result: {
            lines: [
              { num: 1, text: 'File content' },
            ],
          },
        },
      })
    })

    it('refuses a request of another call on the result row', () => {
      const foreign = input({
        sessionUpdate: 'tool_call',
        toolCallId: 'other',
        kind: 'read',
        title: 'Read other.md',
        status: 'pending',
        rawInput: { path: '/project/other.md' },
      }, null, AgentProvider.OPENCODE)
      const row = acpExtractRow({
        resolved: result,
        category: { kind: 'tool_use' },
        span: {
          request: foreign,
          role: 'result',
          visibleRows: { request: true },
        },
      })
      expect(row?.kind).toBe('tool')
      expect(row?.kind === 'tool' && row.call.title).not.toBe('Read other.md')
      expect(JSON.stringify(row)).not.toContain('other.md')
    })

    // An update row states its whole call in its own frame. The request beside it
    // must not fill a field that the update leaves out.
    it('builds an unfinished update row from its own frame alone', () => {
      const update = input({
        sessionUpdate: 'tool_call_update',
        toolCallId: 'pair',
        status: 'in_progress',
      }, null, AgentProvider.OPENCODE)
      const row = acpExtractRow({
        resolved: update,
        category: { kind: 'tool_use' },
        span: {
          request,
          role: 'other',
          visibleRows: { request: true },
        },
      })
      expect(row).toMatchObject({ kind: 'tool', role: 'update', hasRequestRow: true })
      expect(JSON.stringify(row)).not.toContain('README.md')
    })
  })
})

describe('acpNeedsRequest', () => {
  it('answers false for a request frame, also one that states no arguments', () => {
    expect(acpNeedsRequest(input({
      sessionUpdate: 'tool_call',
      toolCallId: 'call',
      kind: 'read',
      status: 'pending',
      rawInput: {},
    }, null, AgentProvider.OPENCODE))).toBe(false)
  })

  it('answers false for an unfinished update', () => {
    expect(acpNeedsRequest(input({
      sessionUpdate: 'tool_call_update',
      toolCallId: 'call',
      status: 'in_progress',
    }, null, AgentProvider.OPENCODE))).toBe(false)
  })

  it.each(['completed', 'failed'])('answers true for a %s update, which merges the request', (status) => {
    expect(acpNeedsRequest(input({
      sessionUpdate: 'tool_call_update',
      toolCallId: 'call',
      status,
    }, null, AgentProvider.OPENCODE))).toBe(true)
  })

  it('answers false for a frame that is no tool frame', () => {
    expect(acpNeedsRequest(input({
      sessionUpdate: 'plan',
      entries: [],
    }, null, AgentProvider.OPENCODE))).toBe(false)
    expect(acpNeedsRequest(input(undefined, null, AgentProvider.OPENCODE))).toBe(false)
  })
})
