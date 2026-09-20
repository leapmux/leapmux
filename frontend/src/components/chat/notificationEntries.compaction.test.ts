import { describe, expect, it } from 'vitest'
import { AgentProvider, MessageSource } from '~/generated/proto/leapmux/v1/agent_pb'
import { parseMessageContent } from '~/lib/messageParser'
import { makeMessage, rawContent } from '~/test-support/messageFactory'
import { compactionContextTokens } from './notificationEntries'
import { claudeCompactionBoundary } from './providers/claude/extractors/notification'
import { codexCompactionBoundary } from './providers/codex/extractors/notification'
import './providers'

// The compaction-boundary reading, after it moved off `messageParser`'s shared
// shape test and onto each provider's own hook. The cases are the ones that guarded
// the shared test: they now prove that the provider that OWNS a shape recognizes it
// and that the other one does not.

function parse(content: unknown, provider: AgentProvider = AgentProvider.CLAUDE_CODE) {
  return parseMessageContent(makeMessage({ source: MessageSource.AGENT, content: rawContent(content), agentProvider: provider }))
}

/** Wrap compaction metadata in the Claude `compact_boundary` system shape. */
function boundary(compactMetadata: Record<string, unknown>) {
  return { type: 'system', subtype: 'compact_boundary', compact_metadata: compactMetadata }
}

describe('claudeCompactionBoundary', () => {
  it('recognizes the compact_boundary system message', () => {
    expect(claudeCompactionBoundary(parse({ type: 'system', subtype: 'compact_boundary' }))).toEqual({
      trigger: undefined,
      pre: undefined,
      post: undefined,
    })
  })

  it('recognizes a microcompaction, which carries no metadata of its own', () => {
    expect(claudeCompactionBoundary(parse({ type: 'system', subtype: 'microcompact_boundary' }))).toEqual({})
  })

  // The two shapes belong to different providers. Claude reading Codex's item would
  // be the cross-provider leak this refactor removes.
  it('refuses a Codex contextCompaction item', () => {
    expect(claudeCompactionBoundary(parse({ item: { type: 'contextCompaction', id: 'compact-1' } }))).toBeNull()
  })

  it('refuses an ordinary system message', () => {
    expect(claudeCompactionBoundary(parse({ type: 'system', subtype: 'status' }))).toBeNull()
  })
})

describe('codexCompactionBoundary', () => {
  const codex = (content: unknown) => parse(content, AgentProvider.CODEX)

  it('recognizes a completed contextCompaction item at the top level', () => {
    expect(codexCompactionBoundary(codex({ item: { type: 'contextCompaction', id: 'compact-1' }, threadId: 't1' }))).not.toBeNull()
  })

  it('recognizes a completed contextCompaction item inside an item/completed notification', () => {
    expect(codexCompactionBoundary(codex({
      method: 'item/completed',
      params: { item: { type: 'contextCompaction', id: 'compact-1' }, threadId: 't1', turnId: 'turn1' },
    }))).not.toBeNull()
  })

  it('refuses an item/completed notification for any other item type', () => {
    expect(codexCompactionBoundary(codex({ method: 'item/completed', params: { item: { type: 'agentMessage', id: 'msg-1' } } }))).toBeNull()
  })

  // `item/started` OPENS the compaction; the grid must not read a post count from it.
  it('refuses the item/started notification that only opens the compaction', () => {
    expect(codexCompactionBoundary(codex({
      method: 'item/started',
      params: { item: { type: 'contextCompaction', id: 'compact-1' } },
    }))).toBeNull()
  })

  it('refuses an item/completed notification with no params', () => {
    expect(codexCompactionBoundary(codex({ method: 'item/completed' }))).toBeNull()
  })

  it('refuses the thread/compacted notification, which carries no boundary', () => {
    expect(codexCompactionBoundary(codex({ method: 'thread/compacted', params: { threadId: 't1', turnId: 'turn1' } }))).toBeNull()
  })
})

describe('compactionContextTokens', () => {
  const post = (content: unknown) => compactionContextTokens(parse(content), AgentProvider.CLAUDE_CODE)

  it('returns post_tokens when the boundary carries it directly', () => {
    expect(post(boundary({ trigger: 'manual', pre_tokens: 105424, post_tokens: 8476 }))).toBe(8476)
  })

  it('derives post from pre_tokens minus tokens_saved when post_tokens is absent', () => {
    expect(post(boundary({ trigger: 'auto', pre_tokens: 100000, tokens_saved: 40000 }))).toBe(60000)
  })

  it('prefers explicit post_tokens over deriving from tokens_saved', () => {
    expect(post(boundary({ pre_tokens: 100000, post_tokens: 8000, tokens_saved: 1 }))).toBe(8000)
  })

  it('reads camelCase keys', () => {
    expect(post({ type: 'system', subtype: 'compact_boundary', compactMetadata: { preTokens: 100000, postTokens: 8000 } })).toBe(8000)
  })

  it('derives from camelCase preTokens minus tokensSaved', () => {
    expect(post({ type: 'system', subtype: 'compact_boundary', compactMetadata: { preTokens: 100000, tokensSaved: 25000 } })).toBe(75000)
  })

  it('states nothing for a message that carries no boundary', () => {
    expect(post({ type: 'system', subtype: 'status' })).toBeUndefined()
  })

  it('states nothing for a boundary whose post cannot be resolved', () => {
    expect(post(boundary({ trigger: 'manual' }))).toBeUndefined()
  })

  // A negative derived post -- `saved` exceeding `pre` -- clamps rather than showing
  // a negative context size.
  it('clamps a derived post that would go below zero', () => {
    expect(post(boundary({ pre_tokens: 10, tokens_saved: 40 }))).toBe(0)
  })

  it('states nothing when the row has no plugin to ask', () => {
    expect(compactionContextTokens(parse(boundary({ post_tokens: 10 })), undefined)).toBeUndefined()
  })
})
