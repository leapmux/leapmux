import type { ParsedMessageContent } from '~/lib/messageParser'
import { describe, expect, it } from 'vitest'
import { CODEX_PERMISSION_PRESETS } from '~/generated/contracts/codex-permission-presets'
import { CODEX_OPTION, CODEX_OPTION_DEFAULT } from '~/generated/contracts/codex-protocol'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { renderDivider } from '~/test-support/messageRenderProbes'
import { providerQuotableText, providerToolCall } from '~/test-support/toolCallFixture'
import { providerFor } from '../registry'
import { input } from '../testUtils'
import { nativeExecSuccessRequest, nativeExecSuccessResult } from './extractors/execute.fixtures'
import { codexNeedsRequest } from './spanRole'
// Side-effect import to register the Codex plugin.
import './plugin'

describe('codex provider capabilities', () => {
  const plugin = providerFor(AgentProvider.CODEX)!

  it('seeds the default collaboration mode and permission profile as provider options on a new agent', () => {
    expect(plugin?.configuration?.defaultProviderOptions).toEqual({
      [CODEX_OPTION.CollaborationMode]: CODEX_OPTION_DEFAULT.CollaborationMode,
      [CODEX_OPTION.PermissionProfile]: CODEX_OPTION_DEFAULT.PermissionProfile,
    })
  })

  it('exposes attachment capabilities', () => {
    expect(plugin?.configuration?.attachments).toEqual({
      text: true,
      image: true,
      pdf: false,
      audio: [
        'audio/wav',
        'audio/x-wav',
        'audio/wave',
        'audio/vnd.wave',
        'audio/mpeg',
        'audio/mp3',
        'audio/mp4',
        'audio/m4a',
        'audio/x-m4a',
        'audio/webm',
        'audio/ogg',
      ],
      binary: false,
    })
  })

  it('keeps FLAC and AAC formats outside the Codex policy', () => {
    expect(plugin.configuration?.attachments?.audio).not.toContain('audio/flac')
    expect(plugin.configuration?.attachments?.audio).not.toContain('audio/x-flac')
    expect(plugin.configuration?.attachments?.audio).not.toContain('audio/aac')
  })
})

describe('codex quotable text', () => {
  it('reads parent.item.text for assistant_text', () => {
    const parent = {
      item: { type: 'agentMessage', text: '  Hello  ' },
    }
    expect(providerQuotableText(AgentProvider.CODEX, parent, {
      category: { kind: 'assistant_text' },
    })).toBe('Hello')
  })

  it('reads parent.item.text for assistant_thinking', () => {
    const parent = {
      item: { type: 'reasoning', text: 'thinking...' },
    }
    expect(providerQuotableText(AgentProvider.CODEX, parent, {
      category: { kind: 'assistant_thinking' },
    })).toBe('thinking...')
  })

  it('reads parent.content string for user_content / plan_execution', () => {
    expect(providerQuotableText(AgentProvider.CODEX, { content: 'hi' }, {
      category: { kind: 'user_content' },
    })).toBe('hi')
    expect(providerQuotableText(AgentProvider.CODEX, { content: 'plan' }, {
      category: { kind: 'plan_execution' },
    })).toBe('plan')
  })

  it('returns null when item has no text', () => {
    const parent = {
      item: { type: 'agentMessage' },
    }
    expect(providerQuotableText(AgentProvider.CODEX, parent, {
      category: { kind: 'assistant_text' },
    })).toBeNull()
  })

  it('returns null for non-quotable categories', () => {
    expect(providerQuotableText(AgentProvider.CODEX, {
      item: { type: 'agentMessage', text: 'x' },
    }, {
      category: { kind: 'hidden' },
    })).toBeNull()
  })
})

describe('codex result divider', () => {
  const plugin = providerFor(AgentProvider.CODEX)!

  // Every provider states a turn end in one shared vocabulary, so the runtime's own
  // status word never reaches the label on its own. Codex said "Turn completed" where
  // the Agent Client Protocol providers said "Turn ended".
  it('maps a completed turn to the shared turn-end label', () => {
    expect(plugin?.transcript.extractDivider!({
      turn: { id: 'turn-1', status: 'completed' },
    }))
      .toEqual({ label: 'Turn ended' })
  })

  it('maps the statuses that mean the reader stopped the turn', () => {
    for (const status of ['interrupted', 'cancelled', 'aborted']) {
      expect(plugin?.transcript.extractDivider!({
        turn: { id: 'turn-1', status },
      }), status)
        .toEqual({ label: 'Turn interrupted' })
    }
  })

  // A word this build does not know still reads as a turn end, with the word itself
  // kept so nothing the runtime reported is lost.
  it('qualifies the turn end with a status this build does not know', () => {
    expect(plugin?.transcript.extractDivider!({
      turn: { id: 'turn-1', status: 'compacted' },
    }))
      .toEqual({ label: 'Turn ended (compacted)' })
  })

  it('maps a failed turn to a danger divider with the error inline', () => {
    expect(plugin?.transcript.extractDivider!({
      turn: {
        status: 'failed',
        error: { message: 'Boom', additionalDetails: 'timeout' },
      },
    }))
      .toEqual({ label: 'Turn failed — Boom', isError: true, detail: 'timeout' })
  })

  // A failure the runtime reports with no error object at all still reads as one.
  it('marks a failed turn that carries no error object', () => {
    expect(plugin?.transcript.extractDivider!({
      turn: { status: 'failed' },
    }))
      .toEqual({ label: 'Turn failed', isError: true })
  })

  it('falls back to "Unknown error" for a failed turn whose error.message is empty', () => {
    // An explicit empty-string message is a present string, so pickString's
    // missing-key fallback does not apply -- guard with `|| 'Unknown error'` so
    // the divider never renders a label-less red row.
    expect(plugin?.transcript.extractDivider!({
      turn: {
        status: 'failed',
        error: { message: '' },
      },
    }))
      .toEqual({ label: 'Turn failed — Unknown error', isError: true })
  })

  it('returns null when the turn carries no status', () => {
    expect(plugin?.transcript.extractDivider!({ turn: {} })).toBeNull()
  })

  it('renders a failed turn as a danger divider through the shared renderer end-to-end', () => {
    // MessageBubble routes result_divider through the shared row extraction, which draws
    // the shared ResultDivider with the inline danger color for a failed turn.
    const { text, isError } = renderDivider(
      {
        turn: {
          status: 'failed',
          error: { message: 'Boom', additionalDetails: 'timeout' },
        },
      },
      AgentProvider.CODEX,
    )
    expect(text).toBe('Turn failed — Boomtimeout')
    expect(isError).toBe(true)
  })
})

describe('codex settings config', () => {
  // The provider-specific settings panel was replaced by the composer's shared
  // settings surface (the status-bar chips and the `[+]` menu); the provider now
  // only declares the configuration those render. These assertions cover the declarative shape that
  // used to be exercised through the deleted panel/trigger-label renderers.
  const plugin = providerFor(AgentProvider.CODEX)!

  it('wires plan mode to the collaboration_mode group', () => {
    expect(plugin?.configuration?.planMode).toMatchObject({
      groupKey: 'collaboration_mode',
      planValue: 'plan',
      defaultValue: CODEX_OPTION_DEFAULT.CollaborationMode,
    })
  })

  it('renders the collaboration_mode "Workflow" group as the trigger mode segment', () => {
    // Not the approval-policy permissionMode -- Codex's mode axis is the Workflow group.
    expect(plugin?.configuration?.triggerModeGroupKey).toBe(CODEX_OPTION.CollaborationMode)
  })

  it('reads the current collaboration mode from optionValues, defaulting when unset', () => {
    expect(plugin?.configuration?.planMode!.currentMode({
      optionValues: { [CODEX_OPTION.CollaborationMode]: 'plan' },
    })).toBe('plan')
    expect(plugin?.configuration?.planMode!.currentMode({})).toBe(CODEX_OPTION_DEFAULT.CollaborationMode)
  })

  it('declares the smart and bypass permission presets from the contract', () => {
    expect(plugin?.controls?.permissionPresets).toEqual({
      smart: CODEX_PERMISSION_PRESETS.smart,
      bypass: CODEX_PERMISSION_PRESETS.bypass,
    })
  })

  it('maps smart to approve for me in the workspace profile', () => {
    expect(plugin?.controls?.permissionPresets?.smart?.sets).toEqual({
      permissionMode: 'auto-review',
      [CODEX_OPTION.PermissionProfile]: ':workspace',
    })
  })

  it('maps bypass to never ask with full access', () => {
    expect(plugin?.controls?.permissionPresets?.bypass?.sets).toEqual({
      permissionMode: 'never',
      [CODEX_OPTION.PermissionProfile]: ':danger-full-access',
    })
  })
})

// Build a plain ParsedMessageContent whose inner message is `inner`; the
// session-metadata hooks read `parsed.parentObject` (getInnerMessage falls back
// to topLevel), so both point at the same object.
function parsed(inner: Record<string, unknown>): ParsedMessageContent {
  return {
    rawText: '',
    topLevel: inner,
    parentObject: inner,
    wrapper: null,
  }
}

describe('codex contextUsageFromMessage', () => {
  const plugin = providerFor(AgentProvider.CODEX)!

  it('extracts context usage from a thread/tokenUsage/updated notification (params.tokenUsage.last)', () => {
    const msg = parsed({
      method: 'thread/tokenUsage/updated',
      params: {
        threadId: 'thread-1',
        turnId: 'turn-1',
        tokenUsage: {
          total: {
            totalTokens: 200,
            inputTokens: 100,
            cachedInputTokens: 25,
            outputTokens: 50,
            reasoningOutputTokens: 9,
          },
          last: {
            totalTokens: 23,
            inputTokens: 10,
            cachedInputTokens: 5,
            outputTokens: 7,
            reasoningOutputTokens: 1,
          },
          modelContextWindow: 4096,
        },
      },
    })
    expect(plugin?.session?.contextUsageFromMessage!(msg)).toEqual({
      inputTokens: 5,
      cacheCreationInputTokens: 0,
      cacheReadInputTokens: 5,
      contextWindow: 4096,
    })
  })

  it('returns null for an unrelated method', () => {
    expect(plugin?.session?.contextUsageFromMessage!(parsed({ method: 'turn/completed', params: {} }))).toBeNull()
  })
})

describe('codex rateLimitsFromMessage', () => {
  const plugin = providerFor(AgentProvider.CODEX)!

  it('reads no rate limit from a stored account/rateLimits/updated row', () => {
    expect(plugin).toBeDefined()
    expect(plugin.session?.rateLimitsFromMessage?.(parsed({
      method: 'account/rateLimits/updated',
      params: {
        rateLimits: {
          primary: { usedPercent: 99, windowDurationMins: 300 },
        },
      },
    }))).toBeUndefined()
  })
})

// Which rows draw from the span's request. A row reads no later message of its span,
// so a request row never needs anything. A typed item states its arguments in every
// frame, so its result needs nothing either. Two results read the request: a native
// code output, which states no script source, and a finished collaboration call, which
// can state an empty prompt, model or target list.
describe('codex needsRequest', () => {
  const plugin = providerFor(AgentProvider.CODEX)!
  const item = (fields: Record<string, unknown>) => ({
    item: { id: 'call', ...fields },
  })
  const needsRequest = (frame: Record<string, unknown>) => codexNeedsRequest(input(frame, undefined, AgentProvider.CODEX))

  it('is the Codex reader', () => {
    expect(plugin.transcript.needsRequest).toBe(codexNeedsRequest)
  })

  it('answers true for a finished collaboration call', () => {
    expect(needsRequest(item({ type: 'collabAgentToolCall', status: 'completed', tool: 'spawnAgent' }))).toBe(true)
    expect(needsRequest(item({ type: 'collabAgentToolCall', status: 'interrupted', tool: 'spawnAgent' }))).toBe(true)
  })

  it('answers false for a running collaboration call', () => {
    expect(needsRequest(item({
      type: 'collabAgentToolCall',
      status: 'inProgress',
      tool: 'spawnAgent',
      prompt: 'Look',
    }))).toBe(false)
  })

  it('answers true for a native code output, and false for its request', () => {
    expect(needsRequest(nativeExecSuccessResult)).toBe(true)
    expect(needsRequest(nativeExecSuccessRequest)).toBe(false)
  })

  it.each([
    [
      'an MCP result',
      {
        type: 'mcpToolCall',
        status: 'completed',
        server: 's',
        tool: 't',
        arguments: { a: 1 },
      },
    ],
    [
      'a running MCP call',
      {
        type: 'mcpToolCall',
        status: 'inProgress',
        server: 's',
        tool: 't',
        arguments: { a: 1 },
      },
    ],
    [
      'an image view',
      { type: 'imageView', status: 'completed', path: '/a.png' },
    ],
    [
      'a finished command',
      { type: 'commandExecution', status: 'completed', command: 'ls' },
    ],
    [
      'a running command',
      { type: 'commandExecution', status: 'inProgress', command: 'ls' },
    ],
    [
      'a finished file change',
      {
        type: 'fileChange',
        status: 'completed',
        changes: [
          {
            path: '/a.ts',
            kind: { type: 'add' },
            diff: 'a',
          },
        ],
      },
    ],
    [
      'a running file change',
      { type: 'fileChange', status: 'inProgress', changes: [] },
    ],
  ])('answers false for %s, which states its own arguments', (_name, fields) => {
    expect(needsRequest(item(fields))).toBe(false)
  })
})

describe('native output without a filesystem pointer', () => {
  it('keeps a valid native result and omits the output path hook', () => {
    const frame = {
      item: {
        type: 'commandExecution',
        id: 'native-call',
        status: 'completed',
        command: 'printf preview',
        aggregatedOutput: 'native inline preview',
        exitCode: 0,
      },
    }
    const call = providerToolCall(AgentProvider.CODEX, frame, {
      spanId: 'native-call',
      spanType: 'commandExecution',
      agentSessionId: 'native-session',
      role: 'result',
    })
    expect(call).not.toBeNull()
    expect(call?.id).toBe('native-call')
    expect(call?.outputFilePaths).toBeUndefined()
    const registered = providerFor(AgentProvider.CODEX)
    expect(registered).toBeDefined()
    expect(registered?.transcript.outputFilePaths).toBeUndefined()
  })
})
