import type { ToolKind } from '../../model/toolKind'
import { describe, expect, it } from 'vitest'
import { CODEX_ITEM, CODEX_METHOD } from '~/generated/contracts/codex-protocol'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { providerToolCall } from '~/test-support/toolCallFixture'
import { invariantViolations } from '~/test-support/toolVocabulary'
import { isUnparsedToolResult } from '../../model/toolCall'
import { providerFor } from '../registry'
import { input } from '../testUtils'
import { codexItemKind } from './extractors/row'
import './plugin'

/*
 * Every item kind and every notification method Codex sends must reach a row the
 * reader can read.
 *
 * The known world is the generated `CODEX_ITEM` and `CODEX_METHOD` tables, which
 * contracts/codex-protocol.json holds because the Go worker dispatches on the same
 * strings. Each sweep walks its table and fails for a name that reaches `unknown`,
 * which is the raw-JSON bubble a reader learns nothing from.
 *
 * This is the case that produced the gap it guards: Codex added `sleep`, the two
 * review-mode markers, `hookPrompt` and `functionCallOutput`, the worker persisted
 * each through its `default` branch, and every one drew raw JSON until the row table
 * learned to state what it is.
 */

/**
 * The payload each name needs to produce its row.
 *
 * An item carrying nothing to show is hidden on purpose, so a sweep over empty
 * payloads would assert that rule rather than the classification this test is about.
 */
const ITEM_PAYLOAD: Record<string, Record<string, unknown>> = {
  [CODEX_ITEM.AgentMessage]: { text: 'Done.' },
  // A `summary` is an array of STRINGS. An array of objects reaches the extractor
  // as no words at all, so a fixture in that shape asserted a classification for a
  // row that never drew.
  [CODEX_ITEM.Reasoning]: { summary: ['Let me consider...'] },
  [CODEX_ITEM.UserMessage]: { text: 'do the thing' },
  [CODEX_ITEM.Plan]: { text: '# Plan\n\n1. Read the code.' },
  [CODEX_ITEM.CommandExecution]: { command: 'ls -1', status: 'completed', aggregatedOutput: 'a.ts', exitCode: 0 },
  [CODEX_ITEM.FileChange]: { status: 'completed', changes: [{ path: '/repo/a.ts', operation: 'update' }] },
  [CODEX_ITEM.McpToolCall]: { status: 'completed', server: 'srv', tool: 'do' },
  [CODEX_ITEM.DynamicToolCall]: { status: 'completed', toolName: 'custom' },
  [CODEX_ITEM.CollabAgentToolCall]: { status: 'completed', agentsStates: [] },
  [CODEX_ITEM.SubAgentActivity]: { kind: 'started', agentThreadId: 'child-thread', agentPath: '/root/child' },
  [CODEX_ITEM.WebSearch]: { action: { type: 'search', query: 'a query' } },
  [CODEX_ITEM.ImageGeneration]: { result: 'aGk=' },
  [CODEX_ITEM.ImageView]: { path: '/repo/a.png' },
  [CODEX_ITEM.ContextCompaction]: {},
  [CODEX_ITEM.Sleep]: { durationMs: 1200 },
  [CODEX_ITEM.EnteredReviewMode]: { review: 'a review' },
  [CODEX_ITEM.ExitedReviewMode]: {},
  [CODEX_ITEM.HookPrompt]: { fragments: [{ hookRunId: 'run-1', text: 'the hook says so' }] },
  [CODEX_ITEM.FunctionCallOutput]: { callId: 'call-1', namespace: 'tools', output: 'the result' },
}

const METHOD_PAYLOAD: Record<string, Record<string, unknown>> = {
  // The worker usually lifts `params.item` to the top level, but it stores these two
  // verbatim as well -- and while `classify` read `parent.item` alone, a row in that
  // shape reached no rule and drew raw JSON.
  [CODEX_METHOD.ItemStarted]: { item: { id: 'i1', type: CODEX_ITEM.CommandExecution, command: 'ls', status: 'inProgress' } },
  [CODEX_METHOD.ItemCompleted]: { item: { id: 'i1', type: CODEX_ITEM.CommandExecution, command: 'ls', status: 'completed', aggregatedOutput: 'a.ts', exitCode: 0 } },
  [CODEX_METHOD.Error]: { error: { message: 'the upstream model refused' } },
  [CODEX_METHOD.Warning]: { message: 'a warning' },
  [CODEX_METHOD.ThreadNameUpdated]: { name: 'Refactoring auth' },
  [CODEX_METHOD.McpServerStartupStatusUpdated]: { name: 'srv', status: 'ready' },
  [CODEX_METHOD.ThreadTokenUsageUpdated]: { tokenUsage: { last: { inputTokens: 10 } } },
  // `plan` is the ARRAY of steps, not an object around one.
  [CODEX_METHOD.TurnPlanUpdated]: { plan: [{ step: 'Inspect messages', status: 'inProgress' }] },
}

const plugin = () => providerFor(AgentProvider.CODEX)!

function classifyItem(type: string): string {
  return plugin().transcript.classify(input({ item: { id: 'i1', type, ...ITEM_PAYLOAD[type] } })).kind
}

function classifyMethod(method: string): string {
  return plugin().transcript.classify(input({ method, params: { threadId: 't1', ...METHOD_PAYLOAD[method] } })).kind
}

/**
 * The item kinds that draw a row of their own rather than a tool row.
 *
 * `userMessage` is hidden rather than drawn: LeapMux writes the reader's own row from
 * its own record, so Codex's echo of it would be the same words twice.
 */
const STRUCTURAL_ITEM: Record<string, string> = {
  [CODEX_ITEM.AgentMessage]: 'assistant_text',
  [CODEX_ITEM.Reasoning]: 'assistant_thinking',
  [CODEX_ITEM.UserMessage]: 'hidden',
  // A proposed plan draws through the shared plan card, which every provider uses.
  [CODEX_ITEM.Plan]: 'assistant_plan',
  // `subAgentActivity` drives the worker's background-task registry and reaches no
  // transcript at all. Only a row an earlier build wrote carries one, and it draws
  // nothing rather than the raw frame.
  [CODEX_ITEM.SubAgentActivity]: 'hidden',
}

describe('codex item vocabulary', () => {
  it('reads the whole item table the contract holds', () => {
    expect(Object.keys(CODEX_ITEM).length).toBeGreaterThanOrEqual(18)
  })

  it.each(Object.values(CODEX_ITEM))('names the %s item', (type) => {
    const kind = classifyItem(type)
    const expected = STRUCTURAL_ITEM[type]
    if (expected) {
      expect(kind).toBe(expected)
      return
    }
    expect(
      kind,
      `the ${type} item reaches no rule, so its row draws raw JSON. Give it a branch `
      + 'in the Codex row table, or hide it there.',
    ).not.toBe('unknown')
  })

  // An item kind from a release later than this one still has to render as something
  // a reader can read. It takes the unknown row, which draws the payload -- the
  // honest answer, and what makes the sweep above worth having.
  it('leaves an item no release declared as unknown', () => {
    expect(classifyItem('anItemFromALaterRelease')).toBe('unknown')
  })

  /**
   * Every item that draws a TOOL row must reach a real kind.
   *
   * This is the sibling of the eight `toolVocabulary` tests, which walk a table of
   * tool NAMES. Codex states no tools: its item TYPE is the whole identity, so this
   * walks that instead. `other` and the unspecified kind are reserved for an item type no
   * release declared -- a row that carries one draws a wrench, the word "Other" and
   * a dump of the item, and identifies nothing Codex did.
   */
  const UNCATEGORIZED = new Set<ToolKind>(['unspecified', 'other'])

  it.each(Object.values(CODEX_ITEM).filter(type => !(type in STRUCTURAL_ITEM) && type !== CODEX_ITEM.ContextCompaction))(
    'gives the %s item a kind of its own',
    (type) => {
      expect(
        UNCATEGORIZED.has(codexItemKind({ type, ...ITEM_PAYLOAD[type] })),
        `the ${type} item takes the uncategorized kind. Give it one in codexItemKind. `
        + 'Introduce a new ToolKind when none of the closed set fits -- there is no '
        + 'uncategorized rendering path.',
      ).toBe(false)
    },
  )

  it('leaves an item no release declared uncategorized', () => {
    expect(codexItemKind({ type: 'anItemFromALaterRelease' })).toBe('other')
  })

  /**
   * The result axis of the same table.
   *
   * `ITEM_PAYLOAD` doubles as the fixture table: each entry is a SUCCESSFUL result
   * frame, and the same extraction a mounted row runs must answer the kind the
   * table states, hold the I1-I6 invariants, and read its payload unless a reason
   * below says it cannot.
   */
  const callOf = (type: string) => providerToolCall(AgentProvider.CODEX, { item: { id: 'i1', type, status: 'completed', ...ITEM_PAYLOAD[type] } }, { spanType: type })

  const NO_RESULT: Record<string, string> = {
    [CODEX_ITEM.ContextCompaction]: 'A compaction boundary, not a tool call: it draws no tool row.',
  }

  const UNPARSED_WITH_REASON: Record<string, string> = {}

  it.each(Object.values(CODEX_ITEM).filter(type => !(type in STRUCTURAL_ITEM) && !(type in NO_RESULT)))(
    'reads the %s result as the kind its table states',
    (type) => {
      const call = callOf(type)
      expect(call, type).not.toBeNull()
      expect(call!.kind, type).toBe(codexItemKind({ type, ...ITEM_PAYLOAD[type] }))
      expect(invariantViolations(call!), type).toEqual([])
      if (isUnparsedToolResult(call!.result))
        expect(UNPARSED_WITH_REASON[type], `the ${type} result stays unparsed without a reason`).toBeTruthy()
    },
  )

  it('documents every result that stays unparsed', () => {
    for (const type of Object.keys(UNPARSED_WITH_REASON)) {
      const call = callOf(type)
      expect(call !== null && call.result !== undefined && isUnparsedToolResult(call.result), type).toBe(true)
    }
  })
})

describe('codex method vocabulary', () => {
  it('reads the whole method table the contract holds', () => {
    expect(Object.keys(CODEX_METHOD).length).toBeGreaterThanOrEqual(17)
  })

  it.each(Object.values(CODEX_METHOD))(
    'names the %s method',
    (method) => {
      expect(
        classifyMethod(method),
        `${method} reaches no rule, so its row draws raw JSON. Give it a notification `
        + 'entry, or hide it.',
      ).not.toBe('unknown')
    },
  )

  // The worker persists a `warning` through its DEFAULT branch, unchanged, so the
  // browser is the only thing that can state it. It drew raw JSON until the classifier
  // claimed it, which left codexNotificationEntry's warning branch unreachable.
  it('states a warning as a notification rather than raw JSON', () => {
    expect(classifyMethod(CODEX_METHOD.Warning)).toBe('notification')
  })
})
