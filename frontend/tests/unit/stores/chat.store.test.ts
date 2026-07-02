import type { AgentChatMessage } from '~/generated/leapmux/v1/agent_pb'
import { create } from '@bufbuild/protobuf'
import { createRoot } from 'solid-js'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createClassifiedEntryCache } from '~/components/chat/chatEntryCache'
import { AgentChatMessageSchema, AgentProvider, ContentCompression, MessagePageAnchor, MessageSource, TodoItemSchema, TodoStatus } from '~/generated/leapmux/v1/agent_pb'
import { createChatStore, MAX_LOADED_CHAT_MESSAGES, MAX_LOADED_CHAT_MESSAGES_CEILING } from '~/stores/chat.store'
import { MESSAGE_PAGE_SIZE } from '~/stores/chatHistoryPaginator'

// Mock workerRpc for loadInitialMessages / loadOlderMessages / loadNewerPage / catchUpToTail
const mockListAgentMessages = vi.fn()
vi.mock('~/api/workerRpc', () => ({
  listAgentMessages: (...args: unknown[]) => mockListAgentMessages(...args),
}))

function makeMessage(id: string, seq: bigint, deliveryError = '') {
  return create(AgentChatMessageSchema, {
    id,
    source: MessageSource.USER,
    content: new TextEncoder().encode(`{"content":"test"}`),
    seq,
    deliveryError,
  })
}

/**
 * A reseq broadcast: an existing message id re-emitted at a NEW (higher) seq, carrying
 * the explicit `previousSeq` marker the worker sets on a notification-thread
 * consolidation move (the seq it moved FROM). addMessage keys its reseq handling on
 * previousSeq, not on inferring a move from a seq change.
 */
function makeReseq(id: string, seq: bigint, previousSeq: bigint) {
  return create(AgentChatMessageSchema, {
    id,
    source: MessageSource.USER,
    content: new TextEncoder().encode(`{"content":"test"}`),
    seq,
    previousSeq,
  })
}

/** Build a message carrying a spanId, to exercise the tool_use ↔ tool_result span index. */
function makeSpanMessage(id: string, seq: bigint, spanId: string, content: string) {
  return create(AgentChatMessageSchema, {
    id,
    source: MessageSource.USER,
    content: new TextEncoder().encode(JSON.stringify({ content })),
    contentCompression: ContentCompression.NONE,
    seq,
    spanId,
  })
}

/**
 * A Claude tool_use opener carrying `spanId` (classifies as kind 'tool_use'). Pass
 * `previousSeq` to model a reseq broadcast that MOVED this row from an older seq.
 */
function makeToolUseSpan(id: string, seq: bigint, spanId: string, previousSeq = 0n, input: Record<string, unknown> = {}) {
  const raw = { type: 'assistant', message: { content: [{ type: 'tool_use', name: 'Read', input }] } }
  return create(AgentChatMessageSchema, {
    id,
    source: MessageSource.AGENT,
    content: new TextEncoder().encode(JSON.stringify(raw)),
    contentCompression: ContentCompression.NONE,
    seq,
    spanId,
    previousSeq,
    agentProvider: AgentProvider.CLAUDE_CODE,
  })
}

/** A Claude tool_result carrying `spanId` (classifies as kind 'tool_result'). */
function makeToolResultSpan(id: string, seq: bigint, spanId: string) {
  const raw = { type: 'user', span_type: 'Read', message: { role: 'user', content: [{ type: 'tool_result', content: 'output', tool_use_id: 't1' }] } }
  return create(AgentChatMessageSchema, {
    id,
    source: MessageSource.USER,
    content: new TextEncoder().encode(JSON.stringify(raw)),
    contentCompression: ContentCompression.NONE,
    seq,
    spanId,
    agentProvider: AgentProvider.CLAUDE_CODE,
  })
}

function makeUserMessage(id: string, seq: bigint, content: string, deliveryError = '', agentProvider?: AgentProvider) {
  return create(AgentChatMessageSchema, {
    id,
    source: MessageSource.USER,
    content: new TextEncoder().encode(JSON.stringify({ content })),
    contentCompression: ContentCompression.NONE,
    seq,
    deliveryError,
    agentProvider,
  })
}

function makeUserMessageWithAttachments(
  id: string,
  seq: bigint,
  content: string,
  attachments: Array<{ filename: string, mime_type: string }>,
  deliveryError = '',
  agentProvider?: AgentProvider,
) {
  return create(AgentChatMessageSchema, {
    id,
    source: MessageSource.USER,
    content: new TextEncoder().encode(JSON.stringify({ content, attachments })),
    contentCompression: ContentCompression.NONE,
    seq,
    deliveryError,
    agentProvider,
  })
}

/** Build a raw assistant message containing a TodoWrite tool_use. */
function makeTodoWriteMessage(
  id: string,
  seq: bigint,
  todos: Array<{ content: string, status: string, activeForm: string }>,
) {
  const raw = {
    type: 'assistant',
    message: {
      content: [{
        type: 'tool_use',
        name: 'TodoWrite',
        input: { todos },
      }],
    },
  }
  return create(AgentChatMessageSchema, {
    id,
    source: MessageSource.AGENT,
    content: new TextEncoder().encode(JSON.stringify(raw)),
    contentCompression: ContentCompression.NONE,
    seq,
  })
}

describe('createChatStore', () => {
  // Reset the RPC mock before every test so a sticky default (e.g. a
  // `mockResolvedValue`, not `...Once`) set by one test can't leak into the next
  // and silently satisfy an unmocked call -- which would mask a missing stub and
  // make the suite order-dependent. Individual tests still arm their own
  // per-call expectations after this.
  beforeEach(() => {
    mockListAgentMessages.mockReset()
  })

  it('should initialize with empty state', () => {
    createRoot((dispose) => {
      const store = createChatStore()
      expect(store.state.messagesByAgent).toEqual({})
      expect(store.messageErrors()).toEqual({})
      expect(store.state.loading).toBe(false)
      dispose()
    })
  })

  it('should return empty array for unknown agent', () => {
    createRoot((dispose) => {
      const store = createChatStore()
      expect(store.getMessages('unknown')).toEqual([])
      dispose()
    })
  })

  it('should set and clear streaming text', () => {
    createRoot((dispose) => {
      const store = createChatStore()
      store.streamingText.set('a1', 'Hello')
      expect(store.streamingText.get('a1')).toBe('Hello')
      store.streamingText.clear('a1')
      expect(store.streamingText.get('a1')).toBe('')
      dispose()
    })
  })

  it('should ignore clearing a command stream that was never created', () => {
    createRoot((dispose) => {
      const store = createChatStore()
      expect(() => store.clearCommandStream('a1', 'rs_missing')).not.toThrow()
      expect(store.getCommandStream('a1', 'rs_missing')).toEqual([])
      dispose()
    })
  })

  it('should set and clear message errors', () => {
    createRoot((dispose) => {
      const store = createChatStore()
      store.setMessageError('msg1', 'offline')
      expect(store.messageErrors().msg1).toBe('offline')
      store.clearMessageError('msg1')
      expect(store.messageErrors().msg1).toBeUndefined()
      dispose()
    })
  })

  it('should seed messageErrors from deliveryError in addMessage', () => {
    createRoot((dispose) => {
      const store = createChatStore()
      store.addMessage('agent1', makeMessage('msg1', 1n, 'worker offline'))
      expect(store.messageErrors().msg1).toBe('worker offline')
      dispose()
    })
  })

  it('should reinsert a thread-merge update when the same ID gets a newer seq', () => {
    createRoot((dispose) => {
      const store = createChatStore()
      store.addMessage('agent1', makeMessage('msg1', 1n))
      store.addMessage('agent1', makeMessage('msg2', 2n))

      // Thread merge: same ID as msg1, bumped seq
      const merged = makeMessage('msg1', 3n)
      store.addMessage('agent1', merged)

      const msgs = store.getMessages('agent1')
      expect(msgs).toHaveLength(2)
      expect(msgs[0].id).toBe('msg2')
      expect(msgs[0].seq).toBe(2n)
      expect(msgs[1].id).toBe('msg1')
      expect(msgs[1].seq).toBe(3n)
      dispose()
    })
  })

  it('should keep a merged server message ahead of trailing optimistic locals', () => {
    createRoot((dispose) => {
      const store = createChatStore()
      store.addMessage('agent1', makeMessage('notif', 1n))
      store.addMessage('agent1', makeUserMessage('local-1', 0n, '/clear'))

      const merged = makeMessage('notif', 5n)
      store.addMessage('agent1', merged)

      const msgs = store.getMessages('agent1')
      expect(msgs).toHaveLength(2)
      expect(msgs[0].id).toBe('notif')
      expect(msgs[0].seq).toBe(5n)
      expect(msgs[1].id).toBe('local-1')
      expect(msgs[1].seq).toBe(0n)
      dispose()
    })
  })

  it('should not set error for message without deliveryError', () => {
    createRoot((dispose) => {
      const store = createChatStore()
      store.addMessage('agent1', makeMessage('msg2', 1n, ''))
      expect(store.messageErrors().msg2).toBeUndefined()
      dispose()
    })
  })

  it('does not orphan a delivery-error annotation for a seq-dedup-discarded fresh message', () => {
    createRoot((dispose) => {
      const store = createChatStore()
      // A server message occupies seq 5.
      store.addMessage('agent1', makeMessage('msg1', 5n))
      // A DIFFERENT-id failed send arrives reusing seq 5 (a delete freed the seq
      // and CreateMessage reassigned MAX(seq)+1 before this client saw the delete).
      // applyFreshMessage DISCARDS it (seq already present under msg1), so it never
      // joins the window -- its delivery error must not leak into the un-capped
      // errors map under an id no row carries.
      store.addMessage('agent1', makeMessage('dup', 5n, 'send failed'))

      const msgs = store.getMessages('agent1')
      expect(msgs).toHaveLength(1)
      expect(msgs[0].id).toBe('msg1')
      expect(store.messageErrors().dup).toBeUndefined()
      dispose()
    })
  })

  it('does not bump the message version on a pure seq-dedup discard', () => {
    createRoot((dispose) => {
      const store = createChatStore()
      store.addMessage('agent1', makeMessage('msg1', 5n))
      const versionBefore = store.getMessageVersion('agent1')
      // A different-id message reusing seq 5 is DISCARDED (seq already present), so
      // the window is byte-identical -- the version must not move, or every replayed
      // duplicate would needlessly wake the auto-scroll effect and the entry cache.
      store.addMessage('agent1', makeMessage('dup', 5n))
      expect(store.getMessages('agent1')).toHaveLength(1)
      expect(store.getMessageVersion('agent1')).toBe(versionBefore)
      dispose()
    })
  })

  it('bumps the message version when a discarded seq still reconciles an optimistic local', () => {
    createRoot((dispose) => {
      const store = createChatStore()
      // A server row holds seq 5, and an optimistic local "hello" is pending.
      store.addMessage('agent1', makeMessage('msg1', 5n))
      store.addMessage('agent1', makeUserMessage('local-1', 0n, 'hello'))
      const versionBefore = store.getMessageVersion('agent1')
      // The echo reuses seq 5 (discarded against msg1) BUT reconciles the local away,
      // so the window DID change (the local is dropped) and the version must bump.
      store.addMessage('agent1', makeUserMessage('server-1', 5n, 'hello'))
      const msgs = store.getMessages('agent1')
      expect(msgs.map(m => m.id)).toEqual(['msg1'])
      expect(store.getMessageVersion('agent1')).toBeGreaterThan(versionBefore)
      dispose()
    })
  })

  it('reconciles a LIVE failed local to its echo and reclaims the orphaned error annotation', () => {
    createRoot((dispose) => {
      const store = createChatStore()
      // A live failure: the local is pending (proto deliveryError empty) but persistFailed
      // set its error ANNOTATION by hand. The local was already reconcilable; the bug was
      // the leaked annotation.
      store.addMessage('agent1', makeUserMessage('local-1', 0n, 'hello'))
      store.setMessageError('local-1', 'Failed to deliver')
      expect(store.messageErrors()['local-1']).toBe('Failed to deliver')
      // The server echoes it -> it WAS delivered: the bubble reconciles to the echo and
      // its orphaned annotation must be reclaimed from the un-capped errors map.
      store.addMessage('agent1', makeUserMessage('server-1', 5n, 'hello'))
      expect(store.getMessages('agent1').map(m => m.id)).toEqual(['server-1'])
      expect(store.messageErrors()['local-1']).toBeUndefined()
      dispose()
    })
  })

  it('reconciles a HYDRATED failed local (proto deliveryError set) to its echo, not a dual bubble', () => {
    createRoot((dispose) => {
      const store = createChatStore()
      // A hydrated failure (post-refresh): the local carries the proto deliveryError field,
      // and addMessage seeds its annotation from it. Previously this was NOT reconcilable,
      // producing a confusing failed-AND-delivered pair.
      store.addMessage('agent1', makeUserMessage('local-1', 0n, 'hello', 'Failed to deliver'))
      expect(store.messageErrors()['local-1']).toBe('Failed to deliver')
      store.addMessage('agent1', makeUserMessage('server-1', 5n, 'hello'))
      expect(store.getMessages('agent1').map(m => m.id)).toEqual(['server-1'])
      expect(store.messageErrors()['local-1']).toBeUndefined()
      dispose()
    })
  })

  it('keeps a genuinely-failed local (no matching echo ever arrives)', () => {
    createRoot((dispose) => {
      const store = createChatStore()
      store.addMessage('agent1', makeUserMessage('local-1', 0n, 'hello', 'Failed to deliver'))
      // A DIFFERENT message echoes; nothing matches "hello", so the failed bubble survives.
      store.addMessage('agent1', makeUserMessage('server-other', 5n, 'different'))
      expect(store.getMessages('agent1').map(m => m.id)).toContain('local-1')
      expect(store.messageErrors()['local-1']).toBe('Failed to deliver')
      dispose()
    })
  })

  it('does not churn on an identical same-seq re-delivery, but does on a real same-seq change', () => {
    createRoot((dispose) => {
      const store = createChatStore()
      store.addMessage('agent1', makeMessage('notif', 5n))
      const versionAfterFirst = store.getMessageVersion('agent1')

      // An IDENTICAL re-delivery (same id, seq, AND content -- a reconnect replay or an
      // at-least-once stream dupe overlapping the loaded window) changes nothing, so the
      // version must not move: bumping it would needlessly re-run auto-scroll, re-check
      // the entry cache, and (in addMessage) force an O(window) span reindex for nothing.
      store.addMessage('agent1', makeMessage('notif', 5n))
      expect(store.getMessages('agent1')).toHaveLength(1)
      expect(store.getMessageVersion('agent1')).toBe(versionAfterFirst)

      // A genuine same-seq in-place update whose CONTENT BYTES changed (a notification
      // consolidation rewrote the body) must fall through and bump -- this is the field
      // sameAgentMessage compares via the serialized form, since protobuf equals()'s
      // instanceof-Uint8Array bytes check misfires across JS realms.
      const bodyChanged = create(AgentChatMessageSchema, {
        id: 'notif',
        source: MessageSource.USER,
        content: new TextEncoder().encode('{"content":"changed"}'),
        seq: 5n,
      })
      store.addMessage('agent1', bodyChanged)
      expect(store.getMessages('agent1')).toHaveLength(1)
      const versionAfterBodyChange = store.getMessageVersion('agent1')
      expect(versionAfterBodyChange).toBeGreaterThan(versionAfterFirst)

      // A same-seq update that changes a NON-content field (a delivery error appears)
      // also bumps.
      store.addMessage('agent1', makeMessage('notif', 5n, 'send failed'))
      expect(store.getMessages('agent1')).toHaveLength(1)
      expect(store.getMessageVersion('agent1')).toBeGreaterThan(versionAfterBodyChange)
      dispose()
    })
  })

  it('should seed messageErrors from deliveryError in setMessages', () => {
    createRoot((dispose) => {
      const store = createChatStore()
      store.setMessages('agent1', [
        makeMessage('msg1', 1n, 'error1'),
        makeMessage('msg2', 2n, ''),
      ])
      expect(store.messageErrors().msg1).toBe('error1')
      expect(store.messageErrors().msg2).toBeUndefined()
      dispose()
    })
  })

  it('should remove message and clear error on removeMessage', () => {
    createRoot((dispose) => {
      const store = createChatStore()
      store.addMessage('agent1', makeMessage('msg1', 1n, 'error'))
      expect(store.getMessages('agent1')).toHaveLength(1)
      expect(store.messageErrors().msg1).toBe('error')

      store.removeMessage('agent1', 'msg1')
      expect(store.getMessages('agent1')).toHaveLength(0)
      expect(store.messageErrors().msg1).toBeUndefined()
      dispose()
    })
  })

  it('should replace a matching optimistic local user message with the persisted server message', () => {
    createRoot((dispose) => {
      const store = createChatStore()
      store.addMessage('agent1', makeUserMessage('local-1', 0n, 'hello', '', AgentProvider.CODEX))
      store.addMessage('agent1', makeUserMessage('server-1', 5n, 'hello', '', AgentProvider.CODEX))

      const msgs = store.getMessages('agent1')
      expect(msgs).toHaveLength(1)
      expect(msgs[0].id).toBe('server-1')
      expect(msgs[0].seq).toBe(5n)
      expect(msgs[0].agentProvider).toBe(AgentProvider.CODEX)
      dispose()
    })
  })

  it('should replace a matching optimistic attachment-only local message with the persisted server message', () => {
    createRoot((dispose) => {
      const store = createChatStore()
      const attachments = [{ filename: 'screenshot.png', mime_type: 'image/png' }]
      store.addMessage('agent1', makeUserMessageWithAttachments('local-1', 0n, '', attachments, '', AgentProvider.CODEX))
      store.addMessage('agent1', makeUserMessageWithAttachments('server-1', 5n, '', attachments, '', AgentProvider.CODEX))

      const msgs = store.getMessages('agent1')
      expect(msgs).toHaveLength(1)
      expect(msgs[0].id).toBe('server-1')
      expect(msgs[0].seq).toBe(5n)
      dispose()
    })
  })

  it('keeps two identical-text optimistic sends as distinct bubbles (only a server echo reconciles)', () => {
    createRoot((dispose) => {
      const store = createChatStore()
      // The same short confirmation sent twice before either echo arrives: each
      // optimistic local (seq 0n) is a NEW send and must render, not collapse the
      // second onto the first.
      store.addMessage('agent1', makeUserMessage('local-a', 0n, 'y'))
      store.addMessage('agent1', makeUserMessage('local-b', 0n, 'y'))
      expect(store.getMessages('agent1').filter(m => m.seq === 0n).map(m => m.id)).toEqual(['local-a', 'local-b'])
      // The first server echo reconciles exactly ONE of them; the other stays
      // pending until its own echo.
      store.addMessage('agent1', makeUserMessage('server-a', 5n, 'y'))
      const remaining = store.getMessages('agent1')
      expect(remaining.some(m => m.id === 'server-a')).toBe(true)
      expect(remaining.filter(m => m.seq === 0n).map(m => m.id)).toEqual(['local-b'])
      dispose()
    })
  })

  it('keeps a still-pending local trailing when a later send echoes first (out-of-order reconcile)', () => {
    createRoot((dispose) => {
      const store = createChatStore()
      store.addMessage('agent1', makeUserMessage('m1', 5n, 'history'))
      // Two DISTINCT sends queued before either echo arrives.
      store.addMessage('agent1', makeUserMessage('local-a', 0n, 'alpha'))
      store.addMessage('agent1', makeUserMessage('local-b', 0n, 'beta'))
      expect(store.getMessages('agent1').map(m => m.id)).toEqual(['m1', 'local-a', 'local-b'])
      // The SECOND send's server echo (higher seq) arrives FIRST. It must reconcile
      // local-b and reinsert in seq order, leaving the still-pending local-a pinned
      // to the tail -- not stranded mid-window between two server messages (which
      // would break the "optimistic locals always trail" invariant that
      // serverMessageEnd / insertServerBySeq / trimOldestEnd all rely on).
      store.addMessage('agent1', makeUserMessage('server-b', 7n, 'beta'))
      const msgs = store.getMessages('agent1')
      expect(msgs.map(m => m.id)).toEqual(['m1', 'server-b', 'local-a'])
      // Invariant: every optimistic local (seq 0n) sits in the trailing suffix.
      const firstLocal = msgs.findIndex(m => m.seq === 0n)
      expect(msgs.slice(firstLocal).every(m => m.seq === 0n)).toBe(true)
      dispose()
    })
  })

  it('full-window replace does not drop a still-pending duplicate against an already-present echo', () => {
    createRoot((dispose) => {
      const store = createChatStore()
      // Two identical sends; the first reconciles LIVE, leaving server-a as a
      // server row in the window and local-b still pending.
      store.addMessage('agent1', makeUserMessage('local-a', 0n, 'y'))
      store.addMessage('agent1', makeUserMessage('local-b', 0n, 'y'))
      store.addMessage('agent1', makeUserMessage('server-a', 5n, 'y'))
      expect(store.getMessages('agent1').filter(m => m.seq === 0n).map(m => m.id)).toEqual(['local-b'])
      // A full-window replace (jump-to-latest / reconnect snapshot) whose page
      // re-lists the already-reconciled server-a but NOT local-b's own echo yet.
      // server-a is already a server row, so it must not consume local-b -- the
      // pending second send must survive (without the already-present discount it
      // would vanish until its real echo lands).
      store.setMessages('agent1', [makeUserMessage('server-a', 5n, 'y')])
      const msgs = store.getMessages('agent1')
      expect(msgs.some(m => m.id === 'server-a')).toBe(true)
      expect(msgs.filter(m => m.seq === 0n).map(m => m.id)).toEqual(['local-b'])
      dispose()
    })
  })

  it('forgetAgent reclaims all per-agent state and leaves other agents intact', () => {
    createRoot((dispose) => {
      const store = createChatStore()
      // Build up state for a1 across every per-agent slice.
      store.setMessages('a1', [makeMessage('m1', 1n, 'boom')], true) // window + hasMoreOlder + initialLoadComplete + an error annotation
      store.appendCommandStream('a1', 's1', 'item/commandExecution/output', 'streaming')
      store.streamingText.set('a1', 'partial')
      store.todos.replace('a1', [create(TodoItemSchema, { content: 'Do', status: TodoStatus.IN_PROGRESS, activeForm: 'Doing' })])
      store.viewportScroll.set('a1', { anchor: { id: 'm1', offsetWithinRow: 12 }, atBottom: false, hasMoreNewer: true })
      store.liveTail.bump('a1', 9n)
      // A second agent, to prove isolation.
      store.setMessages('a2', [makeMessage('n1', 1n)], true)
      store.liveTail.bump('a2', 3n)

      store.forgetAgent('a1')

      // Every per-agent slice for a1 is reclaimed.
      expect(store.getMessages('a1')).toEqual([])
      expect(store.state.hasMoreOlder.a1).toBeUndefined()
      expect(store.state.hasMoreNewer.a1).toBeUndefined()
      expect(store.isInitialLoadComplete('a1')).toBe(false)
      expect(store.messageErrors().m1).toBeUndefined()
      expect(store.getCommandStream('a1', 's1')).toEqual([])
      expect(store.streamingText.get('a1')).toBe('')
      expect(store.todos.get('a1')).toEqual([])
      expect(store.viewportScroll.get('a1')).toBeUndefined()
      expect(store.liveTail.get('a1')).toBe(0n)
      // a2 is untouched.
      expect(store.getMessages('a2')).toHaveLength(1)
      expect(store.isInitialLoadComplete('a2')).toBe(true)
      expect(store.liveTail.get('a2')).toBe(3n)
      dispose()
    })
  })

  it('reconcileAuthoritativeTail drops phantom rows above the tail and clamps the live tail', () => {
    createRoot((dispose) => {
      const store = createChatStore()
      store.setMessages('a1', [makeMessage('m1', 1n), makeMessage('m2', 2n), makeMessage('m3', 3n)], false)
      store.liveTail.bump('a1', 3n)
      // The worker reports the authoritative tail is 2 (m3 was deleted while the
      // client was disconnected and never received the delete).
      store.reconcileAuthoritativeTail('a1', 2n)
      expect(store.getMessages('a1').map(m => m.id)).toEqual(['m1', 'm2'])
      expect(store.liveTail.get('a1')).toBe(2n)
      // window tail (2) now matches the recorded tail, so the affordance clears.
      expect(store.caughtUpToLiveTail('a1')).toBe(true)
      dispose()
    })
  })

  it('reconcileAuthoritativeTail preserves optimistic locals and empties a fully-deleted server window', () => {
    createRoot((dispose) => {
      const store = createChatStore()
      store.setMessages('a1', [makeMessage('m1', 1n)], false)
      store.addMessage('a1', makeUserMessage('local-1', 0n, 'pending'))
      // The entire server history was deleted while away: authoritative tail 0.
      store.reconcileAuthoritativeTail('a1', 0n)
      // The server row m1 is dropped; the still-pending optimistic local survives.
      expect(store.getMessages('a1').map(m => m.id)).toEqual(['local-1'])
      dispose()
    })
  })

  it('reconcileAuthoritativeTail skips a negative latest_seq (worker could not determine the tail)', () => {
    createRoot((dispose) => {
      const store = createChatStore()
      store.setMessages('a1', [makeMessage('m1', 1n), makeMessage('m2', 2n)], false)
      store.liveTail.bump('a1', 2n)
      store.reconcileAuthoritativeTail('a1', -1n)
      // Nothing trimmed, tail untouched -- we don't act on a value we can't trust.
      expect(store.getMessages('a1').map(m => m.id)).toEqual(['m1', 'm2'])
      expect(store.liveTail.get('a1')).toBe(2n)
      dispose()
    })
  })

  it('reconcileAuthoritativeTail PROBES on an indeterminate CatchUpComplete (nudges the live tail past the loaded tail)', () => {
    createRoot((dispose) => {
      const store = createChatStore()
      store.setMessages('a1', [makeMessage('m1', 1n), makeMessage('m2', 2n)], false)
      store.liveTail.bump('a1', 2n)
      expect(store.caughtUpToLiveTail('a1')).toBe(true)
      // CatchUpComplete with an indeterminate (-1) tail + probeIndeterminate=true: the
      // worker couldn't read its tail, so liveTail can't be raised authoritatively. Nudge
      // it one past the loaded tail so the continuous reconcile probes (catchUpToTail)
      // instead of trusting a possibly-partial replay as the tail; settleToWindow clamps
      // the nudge back down if nothing's actually there.
      store.reconcileAuthoritativeTail('a1', -1n, undefined, true)
      expect(store.liveTail.get('a1')).toBe(3n) // windowTail (2) + 1
      expect(store.caughtUpToLiveTail('a1')).toBe(false) // now lagging -> reconcile probes
      // Nothing is trimmed (no authoritative tail to reap against).
      expect(store.getMessages('a1').map(m => m.id)).toEqual(['m1', 'm2'])
      dispose()
    })
  })

  it('reconcileAuthoritativeTail does NOT probe an indeterminate CatchUpStart (default), leaving the tail untouched', () => {
    createRoot((dispose) => {
      const store = createChatStore()
      store.setMessages('a1', [makeMessage('m1', 1n), makeMessage('m2', 2n)], false)
      store.liveTail.bump('a1', 2n)
      // CatchUpStart path (probeIndeterminate defaults false): the replay hasn't run yet,
      // so a probe would race it. Leave liveTail untouched -- catchUpComplete probes.
      store.reconcileAuthoritativeTail('a1', -1n)
      expect(store.liveTail.get('a1')).toBe(2n)
      expect(store.caughtUpToLiveTail('a1')).toBe(true)
      dispose()
    })
  })

  it('reconcileAuthoritativeTail keeps hasMoreNewer set when the window tail still lags the authoritative tail', () => {
    createRoot((dispose) => {
      const store = createChatStore()
      // Scrolled-up window holding [1,2] with the live tail recorded much higher.
      store.setMessages('a1', [makeMessage('m1', 1n), makeMessage('m2', 2n)], false)
      store.liveTail.bump('a1', 100n)
      // Authoritative tail is 50 (rows above 50 deleted while away, but 3..50 still
      // exist beyond the loaded window). No loaded row exceeds 50, so nothing trims,
      // but the recorded tail clamps to 50 and there are still newer messages to fetch.
      store.reconcileAuthoritativeTail('a1', 50n)
      expect(store.getMessages('a1').map(m => m.id)).toEqual(['m1', 'm2'])
      expect(store.liveTail.get('a1')).toBe(50n)
      expect(store.caughtUpToLiveTail('a1')).toBe(false)
      dispose()
    })
  })

  it('reconcileAuthoritativeTail with a reap ceiling exempts a live arrival raced in during catch-up', () => {
    createRoot((dispose) => {
      const store = createChatStore()
      // Replay began with the tail at 3 ([1,2,3] loaded). During catch-up m3 (seq 3)
      // was deleted (tail dropped to 2) and m4 (seq 4) was broadcast LIVE and appended.
      store.setMessages('a1', [makeMessage('m1', 1n), makeMessage('m2', 2n), makeMessage('m3', 3n), makeMessage('m4', 4n)], false)
      store.liveTail.bump('a1', 4n)
      // CatchUpComplete: latest_seq 2, start_tail_seq 3. The band (2, 3] reaps the
      // deleted-during-replay phantom m3; m4 (seq above the start tail) is a live
      // arrival, NOT a missed deletion, so it survives.
      store.reconcileAuthoritativeTail('a1', 2n, 3n)
      expect(store.getMessages('a1').map(m => m.id)).toEqual(['m1', 'm2', 'm4'])
      // The recorded live tail is NOT lowered below the live arrival (the old absolute
      // clamp to latest_seq would have erased it and wedged the affordance).
      expect(store.liveTail.get('a1')).toBe(4n)
      // The window tail (m4) matches the recorded tail, so "new messages below" is
      // correctly clear -- the old behavior reaped m4 and clamped the tail to 2, leaving
      // the window tail at 2 < a recorded tail that a later bump could not restore.
      expect(store.caughtUpToLiveTail('a1')).toBe(true)
      dispose()
    })
  })

  it('reconcileAuthoritativeTail with no ceiling reaps every row beyond the tail (indeterminate-cursor fallback)', () => {
    createRoot((dispose) => {
      const store = createChatStore()
      store.setMessages('a1', [makeMessage('m1', 1n), makeMessage('m2', 2n), makeMessage('m3', 3n)], false)
      store.liveTail.bump('a1', 3n)
      // With no ceiling (the fallback when the resume cursor is unknown) every loaded
      // row beyond the tail is reaped (m2, m3 deleted while disconnected).
      store.reconcileAuthoritativeTail('a1', 1n)
      expect(store.getMessages('a1').map(m => m.id)).toEqual(['m1'])
      expect(store.liveTail.get('a1')).toBe(1n)
      dispose()
    })
  })

  it('reconcileAuthoritativeTail at CatchUpStart bounds the reap by the resume cursor, exempting a post-subscribe live arrival', () => {
    createRoot((dispose) => {
      const store = createChatStore()
      // The client subscribed with its loaded tail at 3 ([1,2,3]). During catch-up m3
      // (seq 3) was deleted server-side (true tail dropped to 2) and m4 (seq 4) raced in
      // LIVE before the CatchUpStart frame. CatchUpStart passes the resume cursor (3) as
      // the ceiling: the (2, 3] band reaps the phantom m3 while m4 (seq above the resume
      // cursor -- arrived AFTER subscribe) survives, instead of the old no-ceiling reap
      // that dropped m4 then re-added it from the replay burst (a flicker).
      store.setMessages('a1', [makeMessage('m1', 1n), makeMessage('m2', 2n), makeMessage('m3', 3n), makeMessage('m4', 4n)], false)
      store.liveTail.bump('a1', 4n)
      store.reconcileAuthoritativeTail('a1', 2n, 3n)
      expect(store.getMessages('a1').map(m => m.id)).toEqual(['m1', 'm2', 'm4'])
      expect(store.liveTail.get('a1')).toBe(4n)
      dispose()
    })
  })

  it('reconcileAuthoritativeTail at CatchUpComplete with an indeterminate start tail uses the resume cursor, NOT a reap-everything fallback', () => {
    createRoot((dispose) => {
      const store = createChatStore()
      // CatchUpComplete arrives with a DETERMINATE latest_seq (2) but an INDETERMINATE
      // start_tail_seq (-1, a failed worker readback). The handler now falls back to the
      // resume cursor (the loaded tail at subscribe, here 3) as the reap ceiling -- the
      // same bound catchUpStart uses -- so the live arrival m4 (seq above the resume
      // cursor) survives while the deleted-during-replay phantom m3 is still reaped.
      // Before the fix the handler passed `undefined`, reaping m4 AND clamping the
      // recorded tail to 2 -- losing the raced-in message (the line-620 reap-everything
      // behavior) until a later event re-discovered it.
      store.setMessages('a1', [makeMessage('m1', 1n), makeMessage('m2', 2n), makeMessage('m3', 3n), makeMessage('m4', 4n)], false)
      store.liveTail.bump('a1', 4n)
      const resumeCursor = 3n // = resumeTails.get(agentId) at subscribe
      store.reconcileAuthoritativeTail('a1', 2n, resumeCursor, true)
      expect(store.getMessages('a1').map(m => m.id)).toEqual(['m1', 'm2', 'm4'])
      expect(store.liveTail.get('a1')).toBe(4n) // live arrival's seq preserved, not clamped to 2
      expect(store.caughtUpToLiveTail('a1')).toBe(true)
      dispose()
    })
  })

  it('addMessage drops a live arrival beyond a catch-up gap rather than tearing the window', () => {
    createRoot((dispose) => {
      const store = createChatStore()
      // Reconnect catch-up: a bounded WatchEvents replay has loaded only [1..50], while
      // CatchUpStart recorded the authoritative start-tail at 100 -- so seqs 51..100 are
      // KNOWN-but-unloaded. hasMoreNewer is false (the reader is following the tail, not
      // scrolled away), which the old hasMoreNewer-only guard treated as "caught up".
      store.setMessages('a1', [makeMessage('m1', 1n), makeMessage('m50', 50n)], false)
      store.liveTail.bump('a1', 100n)
      expect(store.getLastSeq('a1')).toBe(50n)
      expect(store.state.hasMoreNewer.a1 ?? false).toBe(false)

      // A live message at seq 101 (beyond the known start-tail) arrives during catch-up.
      store.addMessage('a1', makeMessage('m101', 101n))

      // It must NOT splice into the window (which would leave a 51..100 gap below it with
      // the affordance wrongly clear and the forward-fill starting past the gap). It is
      // recorded in the live tail and left for catchUpToTail to forward-fill contiguously.
      expect(store.getMessages('a1').map(m => m.id)).toEqual(['m1', 'm50'])
      expect(store.getLastSeq('a1')).toBe(50n)
      expect(store.liveTail.get('a1')).toBe(101n)
      // The window tail (50) still lags the recorded tail (101): the affordance stays lit.
      expect(store.caughtUpToLiveTail('a1')).toBe(false)
      dispose()
    })
  })

  it('addMessage keeps a contiguous replay frame within the known catch-up tail', () => {
    createRoot((dispose) => {
      const store = createChatStore()
      // Same catch-up state: loaded [1..50], known tail 100, hasMoreNewer false.
      store.setMessages('a1', [makeMessage('m1', 1n), makeMessage('m50', 50n)], false)
      store.liveTail.bump('a1', 100n)
      // The replay's next frame (seq 51, WITHIN the known tail) is the contiguous
      // continuation: it must be appended, not dropped as a beyond-tail arrival.
      store.addMessage('a1', makeMessage('m51', 51n))
      expect(store.getMessages('a1').map(m => m.id)).toEqual(['m1', 'm50', 'm51'])
      expect(store.getLastSeq('a1')).toBe(51n)
      dispose()
    })
  })

  it('addMessage appends a contiguous live arrival at a followed tail (no spurious catch-up drop)', () => {
    createRoot((dispose) => {
      const store = createChatStore()
      // Caught up: the recorded tail equals the window tail (no unfilled gap).
      store.setMessages('a1', [makeMessage('m1', 1n), makeMessage('m2', 2n)], false)
      store.liveTail.bump('a1', 2n)
      store.addMessage('a1', makeMessage('m3', 3n))
      expect(store.getMessages('a1').map(m => m.id)).toEqual(['m1', 'm2', 'm3'])
      expect(store.caughtUpToLiveTail('a1')).toBe(true)
      dispose()
    })
  })

  it('resendPendingOutbound drains the queue and sends each via the injected sender', async () => {
    await createRoot(async (dispose) => {
      const store = createChatStore()
      store.pendingOutbound.enqueue('a1', { localId: 'l1', content: 'hi', attachments: [] })
      store.pendingOutbound.enqueue('a1', { localId: 'l2', content: 'yo', attachments: [] })
      store.setMessagePendingLabel('l1', 'Queued')

      const sent: string[] = []
      store.resendPendingOutbound('a1', async (m) => {
        sent.push(m.content)
      })
      await new Promise(resolve => setTimeout(resolve)) // let the fire-and-forget loop finish

      expect(sent).toEqual(['hi', 'yo'])
      expect(store.messagePendingLabels().l1).toBeUndefined() // pending label cleared
      expect(store.pendingOutbound.take('a1')).toEqual([]) // queue drained
      dispose()
    })
  })

  it('resendPendingOutbound stamps a delivery error on a send failure', async () => {
    await createRoot(async (dispose) => {
      const store = createChatStore()
      store.pendingOutbound.enqueue('a1', { localId: 'l1', content: 'boom', attachments: [] })
      store.resendPendingOutbound('a1', async () => {
        throw new Error('network down')
      })
      await new Promise(resolve => setTimeout(resolve))
      expect(store.messageErrors().l1).toBe('Failed to deliver')
      dispose()
    })
  })

  it('failPendingOutbound stamps the given error on every queued message and drains it', () => {
    createRoot((dispose) => {
      const store = createChatStore()
      store.pendingOutbound.enqueue('a1', { localId: 'l1', content: 'a', attachments: [] })
      store.pendingOutbound.enqueue('a1', { localId: 'l2', content: 'b', attachments: [] })
      store.setMessagePendingLabel('l1', 'Queued')
      store.failPendingOutbound('a1', 'Agent failed to start')
      expect(store.messageErrors().l1).toBe('Agent failed to start')
      expect(store.messageErrors().l2).toBe('Agent failed to start')
      expect(store.messagePendingLabels().l1).toBeUndefined()
      expect(store.pendingOutbound.take('a1')).toEqual([])
      dispose()
    })
  })

  it('should preserve attachments when persisting and reloading a failed local message', () => {
    createRoot((dispose) => {
      const store = createChatStore()
      store.persistLocalMessage('agent1', 'local-1', '', 'Failed to deliver', [
        { filename: 'failed.png', mime_type: 'image/png' },
      ])

      store.loadLocalMessages('agent1')

      const msgs = store.getMessages('agent1')
      expect(msgs).toHaveLength(1)
      expect(new TextDecoder().decode(msgs[0].content)).toContain('"filename":"failed.png"')
      dispose()
    })
  })

  it('loadLocalMessages skips a local already in the window (no duplicate, no version churn)', () => {
    createRoot((dispose) => {
      const store = createChatStore()
      store.persistLocalMessage('agent1', 'local-1', 'hi', 'Failed to deliver')
      store.loadLocalMessages('agent1')
      expect(store.getMessages('agent1').filter(m => m.id === 'local-1')).toHaveLength(1)
      const versionAfterLoad = store.getMessageVersion('agent1')

      // A second load (a later cold-start path) must SKIP the already-present
      // local rather than re-add it via the in-place-update branch -- no duplicate
      // and no redundant message-version bump.
      store.loadLocalMessages('agent1')
      expect(store.getMessages('agent1').filter(m => m.id === 'local-1')).toHaveLength(1)
      expect(store.getMessageVersion('agent1')).toBe(versionAfterLoad)
      dispose()
    })
  })

  describe('windowed pagination', () => {
    describe('setMessages with hasMore', () => {
      it('should set hasMoreOlder and initialLoadComplete', () => {
        createRoot((dispose) => {
          const store = createChatStore()
          store.setMessages('a1', [makeMessage('m1', 1n), makeMessage('m2', 2n)], true)
          expect(store.hasOlderMessages('a1')).toBe(true)
          expect(store.isInitialLoadComplete('a1')).toBe(true)
          dispose()
        })
      })

      it('should default hasMore to false', () => {
        createRoot((dispose) => {
          const store = createChatStore()
          store.setMessages('a1', [makeMessage('m1', 1n)])
          expect(store.hasOlderMessages('a1')).toBe(false)
          expect(store.isInitialLoadComplete('a1')).toBe(true)
          dispose()
        })
      })
    })

    describe('getFirstSeq', () => {
      it('should return first message seq', () => {
        createRoot((dispose) => {
          const store = createChatStore()
          store.setMessages('a1', [makeMessage('m1', 5n), makeMessage('m2', 6n)])
          expect(store.getFirstSeq('a1')).toBe(5n)
          dispose()
        })
      })

      it('should return 0n for empty agent', () => {
        createRoot((dispose) => {
          const store = createChatStore()
          expect(store.getFirstSeq('a1')).toBe(0n)
          dispose()
        })
      })
    })

    describe('getLastSeq', () => {
      it('should return last message seq', () => {
        createRoot((dispose) => {
          const store = createChatStore()
          store.setMessages('a1', [makeMessage('m1', 5n), makeMessage('m2', 10n)])
          expect(store.getLastSeq('a1')).toBe(10n)
          dispose()
        })
      })

      it('should return 0n for empty agent', () => {
        createRoot((dispose) => {
          const store = createChatStore()
          expect(store.getLastSeq('a1')).toBe(0n)
          dispose()
        })
      })
    })

    describe('trimOldestEnd (background-tab cap)', () => {
      // The "trims oldest, keeps newest" happy path lives in the
      // 'trimNewestEnd / trimOldestEnd' describe below; this guards the
      // below-threshold no-op that the background-tab cap relies on.
      it('should not trim when below threshold', () => {
        createRoot((dispose) => {
          const store = createChatStore()
          const messages = Array.from({ length: 100 }, (_, i) =>
            makeMessage(`m${i}`, BigInt(i + 1)))
          store.setMessages('a1', messages)
          store.trimOldestEnd('a1', 150)
          expect(store.getMessages('a1')).toHaveLength(100)
          dispose()
        })
      })
    })

    describe('trimOldestToViewport', () => {
      // A window well over the ceiling so each clamp boundary actually trims.
      const fill = (store: ReturnType<typeof createChatStore>, n: number) =>
        store.setMessages('a1', Array.from({ length: n }, (_, i) => makeMessage(`m${i}`, BigInt(i + 1))))

      it('applies the normal cap when keepNewest is below it (following the tail)', () => {
        createRoot((dispose) => {
          const store = createChatStore()
          fill(store, 400)
          store.trimOldestToViewport('a1', 0) // following: no viewport to protect
          const trimmed = store.getMessages('a1')
          expect(trimmed).toHaveLength(MAX_LOADED_CHAT_MESSAGES)
          expect(trimmed.at(-1)!.seq).toBe(400n)
          dispose()
        })
      })

      it('floats the cap up to protect the viewport when keepNewest is between cap and ceiling', () => {
        createRoot((dispose) => {
          const store = createChatStore()
          fill(store, 400)
          store.trimOldestToViewport('a1', 220)
          const trimmed = store.getMessages('a1')
          expect(trimmed).toHaveLength(220) // kept more than the cap so the reader's rows survive
          expect(trimmed[0].seq).toBe(181n)
          expect(trimmed.at(-1)!.seq).toBe(400n)
          dispose()
        })
      })

      it('clamps to the hard ceiling when keepNewest would exceed it (pathological pinned reader)', () => {
        createRoot((dispose) => {
          const store = createChatStore()
          fill(store, MAX_LOADED_CHAT_MESSAGES_CEILING + 300) // over the ceiling so the clamp trims
          store.trimOldestToViewport('a1', MAX_LOADED_CHAT_MESSAGES_CEILING + 999) // keepNewest beyond the ceiling
          expect(store.getMessages('a1')).toHaveLength(MAX_LOADED_CHAT_MESSAGES_CEILING)
          dispose()
        })
      })
    })

    describe('viewport save/restore', () => {
      it('should save and retrieve viewport scroll state', () => {
        createRoot((dispose) => {
          const store = createChatStore()
          const scroll = { anchor: { id: 'm42', offsetWithinRow: 120 }, atBottom: false, hasMoreNewer: false }
          store.viewportScroll.set('a1', scroll)
          expect(store.viewportScroll.get('a1')).toEqual(scroll)
          dispose()
        })
      })

      it('should save at-bottom state', () => {
        createRoot((dispose) => {
          const store = createChatStore()
          const scroll = { atBottom: true, hasMoreNewer: false }
          store.viewportScroll.set('a1', scroll)
          expect(store.viewportScroll.get('a1')).toEqual(scroll)
          dispose()
        })
      })

      it('should clear saved viewport scroll state', () => {
        createRoot((dispose) => {
          const store = createChatStore()
          store.viewportScroll.set('a1', { anchor: { id: 'm1', offsetWithinRow: 120 }, atBottom: false, hasMoreNewer: true })
          store.viewportScroll.clear('a1')
          expect(store.viewportScroll.get('a1')).toBeUndefined()
          dispose()
        })
      })

      it('should return undefined for unsaved agent', () => {
        createRoot((dispose) => {
          const store = createChatStore()
          expect(store.viewportScroll.get('unknown')).toBeUndefined()
          dispose()
        })
      })

      it('saveViewportScrollForRemount persists while the chat window is live', () => {
        createRoot((dispose) => {
          const store = createChatStore()
          // A loaded window marks the agent's chat as live (initialLoadComplete).
          store.setMessages('a1', [makeMessage('m1', 1n)], true)
          const scroll = { anchor: { id: 'm1', offsetWithinRow: 40 }, atBottom: false, hasMoreNewer: false }
          store.saveViewportScrollForRemount('a1', scroll)
          expect(store.viewportScroll.get('a1')).toEqual(scroll)
          dispose()
        })
      })

      it('saveViewportScrollForRemount skips a window that never finished its initial load', () => {
        createRoot((dispose) => {
          const store = createChatStore()
          // No setMessages: initialLoadComplete is ABSENT (not merely deleted). A cold
          // window with no anchorable history has no reader position worth persisting, so
          // the gate skips it -- the falsy branch reached via a missing flag, not a reap.
          store.saveViewportScrollForRemount('a1', { atBottom: true, hasMoreNewer: false })
          expect(store.viewportScroll.get('a1')).toBeUndefined()
          dispose()
        })
      })

      it('saveViewportScrollForRemount does NOT resurrect an entry for a reaped agent', () => {
        createRoot((dispose) => {
          const store = createChatStore()
          store.setMessages('a1', [makeMessage('m1', 1n)], true)
          // Agent close reaps the whole per-agent state first (forgetAgent), and the
          // ChatView unmount-save fires AFTER it -- the save must not leak an entry back.
          store.forgetAgent('a1')
          store.saveViewportScrollForRemount('a1', { atBottom: true, hasMoreNewer: false })
          expect(store.viewportScroll.get('a1')).toBeUndefined()
          expect(store.viewportScroll.byAgent.a1).toBeUndefined()
          dispose()
        })
      })

      it('saveViewportScrollForRemount saves a live window whose TAB was scoped out (workspace switch)', () => {
        createRoot((dispose) => {
          const store = createChatStore()
          // A workspace switch removes the tab (a tabStore concern) but never calls
          // forgetAgent, so the chat window stays live and the switch-away position must
          // survive for the switch-back restore -- the case a tabStore-liveness guard drops.
          store.setMessages('a1', [makeMessage('m1', 1n)], true)
          const scroll = { anchor: { id: 'm1', offsetWithinRow: 8 }, atBottom: false, hasMoreNewer: false }
          store.saveViewportScrollForRemount('a1', scroll)
          expect(store.viewportScroll.get('a1')).toEqual(scroll)
          dispose()
        })
      })
    })

    describe('loadInitialMessages', () => {
      it('should fetch and set messages with hasMore', async () => {
        await createRoot(async (dispose) => {
          const store = createChatStore()
          const messages = Array.from({ length: 50 }, (_, i) =>
            makeMessage(`m${i}`, BigInt(i + 1)))
          mockListAgentMessages.mockResolvedValueOnce({ messages, hasMore: true })

          await store.loadInitialMessages('w1', 'a1')
          expect(store.getMessages('a1')).toHaveLength(50)
          expect(store.hasOlderMessages('a1')).toBe(true)
          expect(store.isInitialLoadComplete('a1')).toBe(true)
          expect(mockListAgentMessages).toHaveBeenCalledWith('w1', { agentId: 'a1', anchor: MessagePageAnchor.LATEST, limit: 50 })
          dispose()
        })
      })

      it('should skip if already loaded', async () => {
        await createRoot(async (dispose) => {
          const store = createChatStore()
          mockListAgentMessages.mockResolvedValueOnce({ messages: [makeMessage('m1', 1n)], hasMore: false })

          await store.loadInitialMessages('w1', 'a1')
          const callCount = mockListAgentMessages.mock.calls.length

          await store.loadInitialMessages('w1', 'a1')
          expect(mockListAgentMessages.mock.calls.length).toBe(callCount) // No new call
          dispose()
        })
      })

      it('should track fetching state', async () => {
        await createRoot(async (dispose) => {
          const store = createChatStore()
          let resolveFn: (value: unknown) => void
          const promise = new Promise(resolve => (resolveFn = resolve))
          mockListAgentMessages.mockReturnValueOnce(promise)

          const loadPromise = store.loadInitialMessages('w1', 'a1')
          expect(store.isFetchingOlder('a1')).toBe(true)

          resolveFn!({ messages: [], hasMore: false })
          await loadPromise

          expect(store.isFetchingOlder('a1')).toBe(false)
          dispose()
        })
      })
    })

    describe('loadOlderMessages', () => {
      it('should prepend older messages', async () => {
        await createRoot(async (dispose) => {
          const store = createChatStore()
          // Set initial messages (seq 51-100)
          const initial = Array.from({ length: 50 }, (_, i) =>
            makeMessage(`m${i + 50}`, BigInt(i + 51)))
          store.setMessages('a1', initial, true)

          // Mock older messages (seq 1-50)
          const older = Array.from({ length: 50 }, (_, i) =>
            makeMessage(`m${i}`, BigInt(i + 1)))
          mockListAgentMessages.mockResolvedValueOnce({ messages: older, hasMore: false })

          await store.loadOlderMessages('w1', 'a1')
          const msgs = store.getMessages('a1')
          expect(msgs).toHaveLength(100)
          expect(msgs[0].seq).toBe(1n)
          expect(msgs[99].seq).toBe(100n)
          expect(store.hasOlderMessages('a1')).toBe(false)
          expect(mockListAgentMessages).toHaveBeenCalledWith('w1', {
            agentId: 'a1',
            anchor: MessagePageAnchor.BEFORE,
            cursorSeq: 51n,
            limit: 50,
          })
          dispose()
        })
      })

      it('should not fetch when hasMoreOlder is false', async () => {
        await createRoot(async (dispose) => {
          const store = createChatStore()
          store.setMessages('a1', [makeMessage('m1', 1n)], false)

          const callCount = mockListAgentMessages.mock.calls.length
          await store.loadOlderMessages('w1', 'a1')
          expect(mockListAgentMessages.mock.calls.length).toBe(callCount)
          dispose()
        })
      })

      it('should not fetch when already fetching', async () => {
        await createRoot(async (dispose) => {
          const store = createChatStore()
          const initial = [makeMessage('m1', 10n)]
          store.setMessages('a1', initial, true)

          let resolveFn: (value: unknown) => void
          const promise = new Promise(resolve => (resolveFn = resolve))
          mockListAgentMessages.mockReturnValueOnce(promise)

          const loadPromise = store.loadOlderMessages('w1', 'a1')
          const callCount = mockListAgentMessages.mock.calls.length

          // Second call should be a no-op
          await store.loadOlderMessages('w1', 'a1')
          expect(mockListAgentMessages.mock.calls.length).toBe(callCount)

          resolveFn!({ messages: [], hasMore: false })
          await loadPromise
          dispose()
        })
      })

      it('should deduplicate overlapping seqs', async () => {
        await createRoot(async (dispose) => {
          const store = createChatStore()
          store.setMessages('a1', [makeMessage('m5', 5n), makeMessage('m6', 6n)], true)

          // Return messages that overlap with existing ones
          const older = [makeMessage('m4', 4n), makeMessage('m5_dup', 5n)]
          mockListAgentMessages.mockResolvedValueOnce({ messages: older, hasMore: false })

          await store.loadOlderMessages('w1', 'a1')
          const msgs = store.getMessages('a1')
          // Should have m4, m5, m6 — not m5_dup
          expect(msgs).toHaveLength(3)
          expect(msgs[0].seq).toBe(4n)
          expect(msgs[1].seq).toBe(5n)
          expect(msgs[2].seq).toBe(6n)
          dispose()
        })
      })

      it('surfaces the delivery error of a failed send carried by a merged older page', async () => {
        await createRoot(async (dispose) => {
          const store = createChatStore()
          store.setMessages('a1', [makeMessage('m5', 5n), makeMessage('m6', 6n)], true)
          // The older page re-loads a historical FAILED send (persisted
          // delivery_error). The merge must set its error annotation -- mirroring
          // the initial-load / addMessage paths -- so the bubble shows the error
          // instead of rendering as a plain message after a scroll-up re-fetch.
          mockListAgentMessages.mockResolvedValueOnce({ messages: [makeMessage('m4', 4n, 'send failed')], hasMore: false })

          await store.loadOlderMessages('w1', 'a1')
          expect(store.getMessages('a1').map(m => m.id)).toEqual(['m4', 'm5', 'm6'])
          expect(store.messageErrors().m4).toBe('send failed')
          dispose()
        })
      })

      it('reconciles via OLDEST when the window holds only locals but hasMoreOlder is set', async () => {
        await createRoot(async (dispose) => {
          mockListAgentMessages.mockClear()
          const store = createChatStore()
          store.setMessages('a1', [], true) // empty window, hasMoreOlder=true
          // Only an undelivered optimistic local remains (distinct content so its
          // signature doesn't match the fetched server echoes).
          store.addMessage('a1', makeUserMessage('local-1', 0n, 'unsent draft'))
          expect(store.getFirstSeq('a1')).toBe(0n)
          expect(store.hasOlderMessages('a1')).toBe(true)

          mockListAgentMessages.mockResolvedValueOnce({ messages: [makeMessage('s1', 1n), makeMessage('s2', 2n)], hasMore: false })
          await store.loadOlderMessages('w1', 'a1')

          // No server cursor to page BEFORE, so it reconciles by loading the
          // earliest real page (anchor OLDEST) instead of a permanent no-op.
          expect(mockListAgentMessages).toHaveBeenCalledWith('w1', { agentId: 'a1', anchor: MessagePageAnchor.OLDEST, limit: 50 })
          const msgs = store.getMessages('a1')
          expect(msgs.map(m => m.id)).toEqual(['s1', 's2', 'local-1']) // earliest page + preserved local
          expect(store.hasOlderMessages('a1')).toBe(false) // now at the start of history
          dispose()
        })
      })
    })

    describe('catchUpToTail', () => {
      it('should fetch a single batch of newer messages', async () => {
        await createRoot(async (dispose) => {
          const store = createChatStore()
          // Pre-load messages seq 1-50
          const initial = Array.from({ length: 50 }, (_, i) =>
            makeMessage(`m${i + 1}`, BigInt(i + 1)))
          store.setMessages('a1', initial)

          // Mock returns seq 51-75 with hasMore: false
          const newer = Array.from({ length: 25 }, (_, i) =>
            makeMessage(`m${i + 51}`, BigInt(i + 51)))
          mockListAgentMessages.mockResolvedValueOnce({ messages: newer, hasMore: false })

          await store.catchUpToTail('w1', 'a1', 50n)
          const msgs = store.getMessages('a1')
          expect(msgs).toHaveLength(75)
          expect(msgs[0].seq).toBe(1n)
          expect(msgs[74].seq).toBe(75n)
          expect(mockListAgentMessages).toHaveBeenLastCalledWith('w1', {
            agentId: 'a1',
            anchor: MessagePageAnchor.AFTER,
            cursorSeq: 50n,
            limit: 50,
          })
          dispose()
        })
      })

      it('should fetch multiple batches until hasMore is false', async () => {
        await createRoot(async (dispose) => {
          mockListAgentMessages.mockClear()
          const store = createChatStore()
          // Pre-load messages seq 1-50
          const initial = Array.from({ length: 50 }, (_, i) =>
            makeMessage(`m${i + 1}`, BigInt(i + 1)))
          store.setMessages('a1', initial)

          // First batch: seq 51-100, hasMore: true
          const batch1 = Array.from({ length: 50 }, (_, i) =>
            makeMessage(`m${i + 51}`, BigInt(i + 51)))
          mockListAgentMessages.mockResolvedValueOnce({ messages: batch1, hasMore: true })

          // Second batch: seq 101-120, hasMore: false
          const batch2 = Array.from({ length: 20 }, (_, i) =>
            makeMessage(`m${i + 101}`, BigInt(i + 101)))
          mockListAgentMessages.mockResolvedValueOnce({ messages: batch2, hasMore: false })

          await store.catchUpToTail('w1', 'a1', 50n)
          const msgs = store.getMessages('a1')
          expect(msgs).toHaveLength(120)
          expect(msgs[0].seq).toBe(1n)
          expect(msgs[119].seq).toBe(120n)

          // Verify two calls with correct cursors
          expect(mockListAgentMessages).toHaveBeenCalledTimes(2)
          expect(mockListAgentMessages).toHaveBeenNthCalledWith(1, 'w1', {
            agentId: 'a1',
            anchor: MessagePageAnchor.AFTER,
            cursorSeq: 50n,
            limit: 50,
          })
          expect(mockListAgentMessages).toHaveBeenNthCalledWith(2, 'w1', {
            agentId: 'a1',
            anchor: MessagePageAnchor.AFTER,
            cursorSeq: 100n,
            limit: 50,
          })
          dispose()
        })
      })

      it('should stop when abort signal is triggered', async () => {
        await createRoot(async (dispose) => {
          mockListAgentMessages.mockClear()
          const store = createChatStore()
          store.setMessages('a1', [makeMessage('m1', 1n)])

          const controller = new AbortController()

          // First batch returns hasMore: true but we abort before the second
          const batch1 = Array.from({ length: 50 }, (_, i) =>
            makeMessage(`m${i + 2}`, BigInt(i + 2)))
          mockListAgentMessages.mockImplementation(async () => {
            // Abort after the first call completes
            controller.abort()
            return { messages: batch1, hasMore: true }
          })

          await store.catchUpToTail('w1', 'a1', 1n, controller.signal)

          // Should have made only one call because signal was aborted
          expect(mockListAgentMessages).toHaveBeenCalledTimes(1)
          dispose()
        })
      })

      it('discards an in-flight page when a user fetch supersedes the catch-up', async () => {
        await createRoot(async (dispose) => {
          mockListAgentMessages.mockClear()
          const store = createChatStore()
          store.setMessages('a1', [makeMessage('m1', 1n)]) // at tail, hasMoreNewer=false

          // Hold catch-up's first AFTER page pending so a user fetch can supersede
          // it mid-flight.
          let resolveCatchUp: (v: { messages: AgentChatMessage[], hasMore: boolean }) => void = () => {}
          const catchUpPage = new Promise<{ messages: AgentChatMessage[], hasMore: boolean }>((res) => {
            resolveCatchUp = res
          })
          mockListAgentMessages
            .mockReturnValueOnce(catchUpPage) // catch-up's first page (stays pending)
            .mockResolvedValueOnce({ messages: [makeMessage('m1', 1n)], hasMore: false }) // jump's LATEST
            .mockResolvedValue({ messages: [], hasMore: false })

          const catchUpDone = store.catchUpToTail('w1', 'a1', 1n)
          await Promise.resolve() // let catch-up reach its first await

          // A user jump-to-latest supersedes catch-up: beginHistoryFetch aborts the
          // catchUpAbort controller synchronously on the call.
          const jumpDone = store.jumpToLatestMessages('w1', 'a1')

          // Resolve catch-up's now-superseded page: it must be DISCARDED, not spliced
          // in behind the jump's fresh window.
          resolveCatchUp({ messages: [makeMessage('m2', 2n), makeMessage('m3', 3n)], hasMore: true })
          await catchUpDone
          await jumpDone

          const ids = store.getMessages('a1').map(m => m.id)
          expect(ids).not.toContain('m2')
          expect(ids).not.toContain('m3')
          expect(ids).toContain('m1')
          dispose()
        })
      })

      it('should handle no new messages', async () => {
        await createRoot(async (dispose) => {
          mockListAgentMessages.mockClear()
          const store = createChatStore()
          store.setMessages('a1', [makeMessage('m1', 1n)])

          mockListAgentMessages.mockResolvedValueOnce({ messages: [], hasMore: false })

          await store.catchUpToTail('w1', 'a1', 1n)
          expect(store.getMessages('a1')).toHaveLength(1)
          expect(mockListAgentMessages).toHaveBeenCalledTimes(1)
          dispose()
        })
      })

      it('jump-to-latest settles the recorded tail when a chased seq has vanished (stalled)', async () => {
        await createRoot(async (dispose) => {
          mockListAgentMessages.mockReset()
          const store = createChatStore()
          store.setMessages('a1', [makeMessage('m1', 1n)])
          // A live message at seq 5 was observed (recorded) then DELETED while we were
          // scrolled away, so the server can no longer give it back.
          store.liveTail.bump('a1', 5n)
          // LATEST re-anchors at [1,2,3]; every AFTER page is empty (seq 5 is gone).
          mockListAgentMessages.mockImplementation(async (_w: string, req: { anchor: MessagePageAnchor }) => {
            if (req.anchor === MessagePageAnchor.LATEST)
              return { messages: [makeMessage('m1', 1n), makeMessage('m2', 2n), makeMessage('m3', 3n)], hasMore: false }
            return { messages: [], hasMore: false } // the vanished seq is unreachable
          })

          await store.jumpToLatestMessages('w1', 'a1')

          // A stalled round clamps the recorded tail DOWN to the window, so the
          // "new messages below" affordance can resolve instead of wedging forever.
          expect(store.liveTail.get('a1')).toBe(3n)
          expect(store.caughtUpToLiveTail('a1')).toBe(true)
          dispose()
        })
      })

      it('jump-to-latest keeps hasMoreNewer (no clamp) when a broadcast storm outruns the fill bound', async () => {
        await createRoot(async (dispose) => {
          mockListAgentMessages.mockReset()
          const store = createChatStore()
          store.setMessages('a1', [makeMessage('m1', 1n)])
          // The recorded tail sits far ahead and stays there (an ongoing storm).
          store.liveTail.bump('a1', 100n)
          // LATEST re-anchors at [1]; each AFTER page advances the window by exactly
          // one, so EVERY fill round makes progress yet never catches the tail.
          mockListAgentMessages.mockImplementation(async (_w: string, req: { anchor: MessagePageAnchor, cursorSeq?: bigint }) => {
            if (req.anchor === MessagePageAnchor.LATEST)
              return { messages: [makeMessage('m1', 1n)], hasMore: false }
            const cursor = req.cursorSeq ?? 0n
            // retryDedupStall probes just below the recorded tail (~99): unreachable.
            if (cursor >= 50n)
              return { messages: [], hasMore: false }
            const next = cursor + 1n
            return { messages: [makeMessage(`m${next}`, next)], hasMore: false }
          })

          await store.jumpToLatestMessages('w1', 'a1')

          // Still short of the live tail after the bound, but the gap is REACHABLE:
          // the recorded tail is NOT clamped away and hasMoreNewer stays set so a
          // later loadNewerPage / reconnect reconcile can pull it contiguously.
          expect(store.hasNewerMessages('a1')).toBe(true)
          expect(store.liveTail.get('a1')).toBe(100n)
          expect(store.caughtUpToLiveTail('a1')).toBe(false)
          dispose()
        })
      })

      it('should be a no-op when scrolled away from the tail (hasMoreNewer)', async () => {
        await createRoot(async (dispose) => {
          mockListAgentMessages.mockClear()
          const store = createChatStore()
          // 200 messages, then trim the newest end so hasMoreNewer is set.
          store.setMessages('a1', Array.from({ length: 200 }, (_, i) => makeMessage(`m${i}`, BigInt(i + 1))))
          store.trimNewestEnd('a1', 150)
          expect(store.hasNewerMessages('a1')).toBe(true)

          await store.catchUpToTail('w1', 'a1', store.getLastSeq('a1'))
          expect(mockListAgentMessages).not.toHaveBeenCalled()
          dispose()
        })
      })

      it('resumeDeferredTailFill stops fetching when its WatchEvents signal is already aborted', async () => {
        await createRoot(async (dispose) => {
          mockListAgentMessages.mockReset()
          const store = createChatStore()
          store.setMessages('a1', [makeMessage('m1', 1n)])
          // Arm the exhaustion-forced deferral: an ongoing broadcast storm (recorded tail
          // far ahead) where every AFTER page advances by one but never catches up, so the
          // bounded fill runs out of attempts while still advancing -> tailFillDeferred set.
          store.liveTail.bump('a1', 100n)
          mockListAgentMessages.mockImplementation(async (_w: string, req: { anchor: MessagePageAnchor, cursorSeq?: bigint }) => {
            if (req.anchor === MessagePageAnchor.LATEST)
              return { messages: [makeMessage('m1', 1n)], hasMore: false }
            const cursor = req.cursorSeq ?? 0n
            if (cursor >= 50n)
              return { messages: [], hasMore: false }
            const next = cursor + 1n
            return { messages: [makeMessage(`m${next}`, next)], hasMore: false }
          })
          await store.jumpToLatestMessages('w1', 'a1')
          expect(store.isTailFillDeferred('a1')).toBe(true)

          // Resume with an ALREADY-ABORTED subscription signal (the workspace switched /
          // worker changed): the loop must abort after at most its first probe rather than
          // draining the storm against a worker the reader navigated away from.
          mockListAgentMessages.mockClear()
          const controller = new AbortController()
          controller.abort()
          await store.resumeDeferredTailFill('w1', 'a1', controller.signal)
          expect(mockListAgentMessages.mock.calls.length).toBeLessThanOrEqual(1)
          dispose()
        })
      })

      it('stops the loop when a concurrent trim flips hasMoreNewer mid-loop', async () => {
        await createRoot(async (dispose) => {
          mockListAgentMessages.mockReset()
          const store = createChatStore()
          // At the tail (hasMoreNewer false) with room to trim.
          store.setMessages('a1', [makeMessage('m1', 1n), makeMessage('m2', 2n), makeMessage('m3', 3n)])
          expect(store.hasNewerMessages('a1')).toBe(false)

          // The first forward page reports more pages, but as it lands a
          // concurrent older-history load trims the newest end -> hasMoreNewer.
          mockListAgentMessages.mockImplementationOnce(async () => {
            store.trimNewestEnd('a1', 1) // window -> [m1], hasMoreNewer = true
            return { messages: [makeMessage('m4', 4n)], hasMore: true }
          })
          // A second iteration would consume this page; the loop must NOT reach it.
          mockListAgentMessages.mockResolvedValue({ messages: [makeMessage('m99', 99n)], hasMore: false })

          await store.catchUpToTail('w1', 'a1', 3n)
          // Exactly one fetch: the re-checked hasMoreNewer broke the loop.
          expect(mockListAgentMessages).toHaveBeenCalledTimes(1)
          expect(store.getMessages('a1').some(m => m.id === 'm99')).toBe(false)
          dispose()
        })
      })

      it('caps each page at the CEILING (not the base), sparing a scrolled-up reader on reconnect', async () => {
        await createRoot(async (dispose) => {
          mockListAgentMessages.mockReset()
          const store = createChatStore()
          // Start at the tail with 50 messages (seq 1-50).
          store.setMessages('a1', Array.from({ length: 50 }, (_, i) => makeMessage(`m${i + 1}`, BigInt(i + 1))))

          // Three forward pages of 50 (seq 51-200) -- past MAX_LOADED (150) but under
          // the ceiling (1200).
          for (let p = 0; p < 3; p++) {
            const start = 51 + p * 50
            const page = Array.from({ length: 50 }, (_, i) => makeMessage(`m${start + i}`, BigInt(start + i)))
            mockListAgentMessages.mockResolvedValueOnce({ messages: page, hasMore: p < 2 })
          }

          await store.catchUpToTail('w1', 'a1', 50n)

          const msgs = store.getMessages('a1')
          // catch-up caps the oldest end at the CEILING, not the base -- consistent with
          // loadNewerPage -- so a reconnect replay never reaps a scrolled-up reader's
          // older buffer (nor the rows below their anchor). 200 is under the ceiling, so
          // nothing is trimmed; the window grows to 200 and stays bounded by the ceiling.
          expect(msgs.length).toBe(200)
          expect(msgs.length).toBeLessThanOrEqual(MAX_LOADED_CHAT_MESSAGES_CEILING)
          expect(msgs[0].seq).toBe(1n) // oldest kept -- not reaped to the base
          expect(msgs.at(-1)!.seq).toBe(200n)
          expect(store.getLastSeq('a1')).toBe(200n)
          dispose()
        })
      })

      it('caps at the CEILING when a reconnect replay genuinely exceeds it (live tail retained, oldest trimmed)', async () => {
        await createRoot(async (dispose) => {
          mockListAgentMessages.mockReset()
          const store = createChatStore()
          // Seed just under the ceiling (seq 1..1150).
          const seed = MAX_LOADED_CHAT_MESSAGES_CEILING - 50
          store.setMessages('a1', Array.from({ length: seed }, (_, i) => makeMessage(`m${i + 1}`, BigInt(i + 1))))
          // Replay 3 forward pages of 50 (seq seed+1 .. seed+150) -> would reach 1300.
          for (let p = 0; p < 3; p++) {
            const start = seed + 1 + p * 50
            const pg = Array.from({ length: 50 }, (_, i) => makeMessage(`m${start + i}`, BigInt(start + i)))
            mockListAgentMessages.mockResolvedValueOnce({ messages: pg, hasMore: p < 2 })
          }

          await store.catchUpToTail('w1', 'a1', BigInt(seed))

          const msgs = store.getMessages('a1')
          // The per-page ceiling trim binds: the window is capped at the ceiling, the
          // newest rows (the live tail) are retained, and the oldest end is trimmed.
          expect(msgs.length).toBe(MAX_LOADED_CHAT_MESSAGES_CEILING)
          expect(msgs.at(-1)!.seq).toBe(BigInt(seed + 150)) // live tail kept
          expect(msgs[0].seq).toBeGreaterThan(1n) // oldest trimmed to the ceiling
          expect(store.hasOlderMessages('a1')).toBe(true)
          dispose()
        })
      })
    })

    describe('atWindowCeiling', () => {
      // The filler must stop a full page BEFORE the hard ceiling so its last allowed
      // fetch can't cross it and drop the live tail -- so the threshold is
      // CEILING - MESSAGE_PAGE_SIZE.
      const threshold = MAX_LOADED_CHAT_MESSAGES_CEILING - MESSAGE_PAGE_SIZE

      it('trips a full page BEFORE the hard ceiling', () => {
        createRoot((dispose) => {
          const store = createChatStore()
          expect(store.atWindowCeiling('a1')).toBe(false) // empty window
          store.setMessages('a1', Array.from({ length: threshold - 1 }, (_, i) => makeMessage(`m${i}`, BigInt(i + 1))))
          expect(store.atWindowCeiling('a1')).toBe(false) // one short of the page-margin threshold
          store.addMessage('a1', makeMessage('edge', BigInt(threshold)))
          expect(store.atWindowCeiling('a1')).toBe(true) // at CEILING - MESSAGE_PAGE_SIZE
          dispose()
        })
      })

      it('counts SERVER rows only -- trailing optimistic locals do not push it over', () => {
        createRoot((dispose) => {
          const store = createChatStore()
          // One server row short of the threshold plus a trailing optimistic local (seq
          // 0n): the local must NOT count (the trims cap server rows). If atWindowCeiling
          // used the array length it would wrongly read the threshold.
          store.setMessages('a1', [
            ...Array.from({ length: threshold - 1 }, (_, i) => makeMessage(`m${i}`, BigInt(i + 1))),
            makeMessage('local', 0n),
          ])
          expect(store.atWindowCeiling('a1')).toBe(false) // only threshold-1 SERVER rows
          dispose()
        })
      })
    })

    describe('trimNewestEnd / trimOldestEnd', () => {
      it('trims the newest end and sets hasMoreNewer when older history was loaded', () => {
        createRoot((dispose) => {
          const store = createChatStore()
          store.setMessages('a1', Array.from({ length: 200 }, (_, i) => makeMessage(`m${i}`, BigInt(i + 1))))
          store.trimNewestEnd('a1', 150)
          const msgs = store.getMessages('a1')
          expect(msgs).toHaveLength(150)
          expect(msgs[0].seq).toBe(1n) // kept the oldest end
          expect(msgs.at(-1)!.seq).toBe(150n)
          expect(store.hasNewerMessages('a1')).toBe(true)
          dispose()
        })
      })

      it('trims the oldest end and sets hasMoreOlder when newer messages were appended', () => {
        createRoot((dispose) => {
          const store = createChatStore()
          store.setMessages('a1', Array.from({ length: 200 }, (_, i) => makeMessage(`m${i}`, BigInt(i + 1))))
          store.trimOldestEnd('a1', 150)
          const msgs = store.getMessages('a1')
          expect(msgs).toHaveLength(150)
          expect(msgs[0].seq).toBe(51n) // kept the newest end
          expect(msgs.at(-1)!.seq).toBe(200n)
          expect(store.hasOlderMessages('a1')).toBe(true)
          expect(store.hasNewerMessages('a1')).toBe(false)
          dispose()
        })
      })

      it('preserves a trailing optimistic local (seq 0n) when trimming the newest end', () => {
        createRoot((dispose) => {
          const store = createChatStore()
          const server = Array.from({ length: 200 }, (_, i) => makeMessage(`m${i}`, BigInt(i + 1)))
          // An undelivered optimistic local sits at the tail (seq 0n).
          store.setMessages('a1', [...server, makeMessage('local-1', 0n)])
          store.trimNewestEnd('a1', 150)
          const msgs = store.getMessages('a1')
          // 150 oldest server messages plus the rescued local — the local is
          // never on the server and can't be re-fetched, so it must survive.
          expect(msgs).toHaveLength(151)
          expect(msgs[0].seq).toBe(1n)
          expect(msgs.at(-1)!.id).toBe('local-1')
          expect(store.hasNewerMessages('a1')).toBe(true)
          dispose()
        })
      })

      it('does NOT flag hasMoreNewer when only trailing locals push over the cap', () => {
        createRoot((dispose) => {
          const store = createChatStore()
          // Exactly the cap of SERVER messages (seq 1..150): the window already
          // IS the live tail. An undelivered optimistic local pushes the length
          // to 151, over the cap -- but no server message gets trimmed.
          const server = Array.from({ length: 150 }, (_, i) => makeMessage(`m${i}`, BigInt(i + 1)))
          store.setMessages('a1', [...server, makeMessage('local-1', 0n)])
          store.trimNewestEnd('a1', 150)
          const msgs = store.getMessages('a1')
          // The window is unchanged (all 150 server + the local kept) ...
          expect(msgs).toHaveLength(151)
          expect(msgs[0].seq).toBe(1n)
          expect(msgs.at(-1)!.id).toBe('local-1')
          // ... so it is still at the tail: hasMoreNewer must stay false, else
          // live messages would be wrongly dropped and the tail UI hidden.
          expect(store.hasNewerMessages('a1')).toBe(false)
          dispose()
        })
      })

      it('does NOT trim server messages (or flag hasMoreOlder) on the newer end when only trailing locals push over the cap', () => {
        createRoot((dispose) => {
          const store = createChatStore()
          // Exactly the cap of SERVER messages plus an undelivered local at the
          // tail: length 151 > 150, but the server portion already fits.
          const server = Array.from({ length: 150 }, (_, i) => makeMessage(`m${i}`, BigInt(i + 1)))
          store.setMessages('a1', [...server, makeMessage('local-1', 0n)])
          store.trimOldestEnd('a1', 150)
          const msgs = store.getMessages('a1')
          // No server message is dropped (a plain slice(-150) would have dropped
          // seq 1 to make room for the local), and hasMoreOlder stays false.
          expect(msgs).toHaveLength(151)
          expect(msgs[0].seq).toBe(1n)
          expect(msgs.at(-1)!.id).toBe('local-1')
          expect(store.hasOlderMessages('a1')).toBe(false)
          dispose()
        })
      })

      it('keeps the newest maxCount SERVER messages alongside trailing locals when over the cap', () => {
        createRoot((dispose) => {
          const store = createChatStore()
          // 200 server messages plus a trailing local. A plain slice(-150) would
          // keep only 149 server + the local; the window must hold the full 150
          // server budget plus the local instead.
          const server = Array.from({ length: 200 }, (_, i) => makeMessage(`m${i}`, BigInt(i + 1)))
          store.setMessages('a1', [...server, makeMessage('local-1', 0n)])
          store.trimOldestEnd('a1', 150)
          const msgs = store.getMessages('a1')
          expect(msgs).toHaveLength(151)
          expect(msgs[0].seq).toBe(51n) // newest 150 server messages
          expect(msgs[149].seq).toBe(200n)
          expect(msgs.at(-1)!.id).toBe('local-1')
          expect(store.hasOlderMessages('a1')).toBe(true)
          dispose()
        })
      })
    })

    describe('span index stays window-scoped', () => {
      it('drops span entries for messages trimmed out of the window', () => {
        createRoot((dispose) => {
          const store = createChatStore()
          const filler = Array.from({ length: 198 }, (_, i) => makeMessage(`m${i}`, BigInt(i + 1)))
          const opener = makeSpanMessage('op', 199n, 's-new', 'OPENER')
          const result = makeSpanMessage('res', 200n, 's-new', 'RESULT')
          store.setMessages('a1', [...filler, opener, result])
          // Indexed and resolvable while in the window.
          expect(store.getToolUseParsedBySpanId('a1', 's-new')?.parentObject?.content).toBe('OPENER')
          expect(store.getToolResultParsedBySpanId('a1', 's-new')?.parentObject?.content).toBe('RESULT')
          // Trimming the newest end (older history loaded) evicts seq 199/200.
          store.trimNewestEnd('a1', 150)
          expect(store.getMessages('a1').some(m => m.spanId === 's-new')).toBe(false)
          // The span index must not retain the trimmed opener/result — otherwise
          // it grows unbounded across a long scroll-through and leaks the messages.
          expect(store.getToolUseParsedBySpanId('a1', 's-new')).toBeUndefined()
          expect(store.getToolResultParsedBySpanId('a1', 's-new')).toBeUndefined()
          dispose()
        })
      })

      it('does not swap opener/result when the opener is prepended after its result', async () => {
        await createRoot(async (dispose) => {
          const store = createChatStore()
          // Window holds only the tool_result (seq 51); its opener (seq 50) is
          // older. With no opener loaded yet, the insertion-order heuristic
          // initially files the result as the opener.
          const result = makeSpanMessage('res', 51n, 's1', 'RESULT')
          const filler = Array.from({ length: 49 }, (_, i) => makeMessage(`m${i}`, BigInt(i + 52)))
          store.setMessages('a1', [result, ...filler], true)
          // Scroll up: the older page carries the real opener (seq 50).
          const opener = makeSpanMessage('op', 50n, 's1', 'OPENER')
          mockListAgentMessages.mockResolvedValueOnce({ messages: [opener], hasMore: false })
          await store.loadOlderMessages('w1', 'a1')
          // After the prepend + reindex over the seq-ascending window, opener and
          // result are correctly separated rather than swapped.
          expect(store.getToolUseParsedBySpanId('a1', 's1')?.parentObject?.content).toBe('OPENER')
          expect(store.getToolResultParsedBySpanId('a1', 's1')?.parentObject?.content).toBe('RESULT')
          dispose()
        })
      })

      it('routes a tool_result that arrives before its tool_use by classification, not arrival order', () => {
        createRoot((dispose) => {
          const store = createChatStore()
          // Live (incremental addMessage) delivery, OUT OF ORDER: the result is
          // appended before its opener and nothing triggers a reindex. Routing by
          // classification keeps the result on the result side rather than
          // misfiling it as the opener (the old insertion-order heuristic would).
          store.addMessage('a1', makeToolResultSpan('res', 51n, 's1'))
          // With only the result seen, the opener lookup is empty (not the result).
          expect(store.getToolUseParsedBySpanId('a1', 's1')).toBeUndefined()
          expect(store.getToolResultParsedBySpanId('a1', 's1')?.parentObject?.type).toBe('user')
          // The opener arrives afterward (lower seq); it must land on the opener side.
          store.addMessage('a1', makeToolUseSpan('op', 50n, 's1'))
          expect(store.getToolUseParsedBySpanId('a1', 's1')?.parentObject?.type).toBe('assistant')
          expect(store.getToolResultParsedBySpanId('a1', 's1')?.parentObject?.type).toBe('user')
          dispose()
        })
      })

      it('drops span entries when a tool message is removed (messageDeleted)', () => {
        createRoot((dispose) => {
          const store = createChatStore()
          store.setMessages('a1', [makeToolUseSpan('op', 50n, 's1'), makeToolResultSpan('res', 51n, 's1')])
          expect(store.getToolUseParsedBySpanId('a1', 's1')?.parentObject?.type).toBe('assistant')
          // A messageDeleted broadcast removes the opener. Like trim/prepend/merge,
          // removal must reindex so the span index can't keep resolving the
          // now-deleted tool_use (a window-scoped leak otherwise).
          store.removeMessage('a1', 'op')
          expect(store.getMessages('a1').some(m => m.id === 'op')).toBe(false)
          expect(store.getToolUseParsedBySpanId('a1', 's1')).toBeUndefined()
          // The surviving result stays indexed.
          expect(store.getToolResultParsedBySpanId('a1', 's1')?.parentObject?.type).toBe('user')
          dispose()
        })
      })

      it('does not index a span for a message discarded by the seq dedup', () => {
        createRoot((dispose) => {
          const store = createChatStore()
          // A server tool_result occupies seq 51 (span s1).
          store.setMessages('a1', [makeToolResultSpan('res', 51n, 's1')])
          expect(store.getToolResultParsedBySpanId('a1', 's1')?.parentObject?.type).toBe('user')
          // A re-broadcast arrives with the SAME seq under a DIFFERENT id and span.
          // addMessage's dedup short-circuit discards it (seq 51 already present),
          // so it never enters the window -- and its span (s2) must NOT be indexed,
          // or the lookup would resolve to a never-rendered message no reindex heals.
          store.addMessage('a1', makeToolResultSpan('res-dup', 51n, 's2'))
          expect(store.getMessages('a1').some(m => m.id === 'res-dup')).toBe(false)
          expect(store.getToolResultParsedBySpanId('a1', 's2')).toBeUndefined()
          dispose()
        })
      })
    })

    describe('loadOlderMessages trims the newest end', () => {
      it('grows a single older page without trimming (below the ceiling)', async () => {
        await createRoot(async (dispose) => {
          const store = createChatStore()
          // A modest window (seq 51..200): well under the ceiling, so a scroll-up
          // page GROWS the window instead of trimming back to the base -- this is
          // what lets the visible buffer accumulate in a hidden-heavy stretch.
          const initial = Array.from({ length: MAX_LOADED_CHAT_MESSAGES }, (_, i) => makeMessage(`m${i + 50}`, BigInt(i + 51)))
          store.setMessages('a1', initial, true)
          const older = Array.from({ length: 50 }, (_, i) => makeMessage(`o${i}`, BigInt(i + 1)))
          mockListAgentMessages.mockResolvedValueOnce({ messages: older, hasMore: true })

          await store.loadOlderMessages('w1', 'a1')
          const msgs = store.getMessages('a1')
          expect(msgs).toHaveLength(MAX_LOADED_CHAT_MESSAGES + 50) // grew, not capped at the base
          expect(msgs[0].seq).toBe(1n)
          expect(msgs.at(-1)!.seq).toBe(200n) // newest NOT trimmed
          expect(store.hasNewerMessages('a1')).toBe(false) // nothing dropped from the newest end
          dispose()
        })
      })

      it('caps the window at the ceiling and sets hasMoreNewer once the buffer is full', async () => {
        await createRoot(async (dispose) => {
          const store = createChatStore()
          // The scrolled-up buffer is already full (ceiling-sized, seq 51..1250).
          const initial = Array.from({ length: MAX_LOADED_CHAT_MESSAGES_CEILING }, (_, i) => makeMessage(`m${i + 50}`, BigInt(i + 51)))
          store.setMessages('a1', initial, true)
          // Older page (seq 1..50) pushes total over the ceiling.
          const older = Array.from({ length: 50 }, (_, i) => makeMessage(`o${i}`, BigInt(i + 1)))
          mockListAgentMessages.mockResolvedValueOnce({ messages: older, hasMore: true })

          await store.loadOlderMessages('w1', 'a1')
          const msgs = store.getMessages('a1')
          expect(msgs).toHaveLength(MAX_LOADED_CHAT_MESSAGES_CEILING)
          expect(msgs[0].seq).toBe(1n) // kept the oldest end the user scrolled to
          expect(msgs.at(-1)!.seq).toBe(BigInt(MAX_LOADED_CHAT_MESSAGES_CEILING))
          expect(store.hasNewerMessages('a1')).toBe(true) // newest end finally trimmed
          expect(store.hasOlderMessages('a1')).toBe(true)
          dispose()
        })
      })

      it('pages before the first SERVER seq, skipping a leading optimistic local', async () => {
        await createRoot(async (dispose) => {
          mockListAgentMessages.mockClear()
          const store = createChatStore()
          // Degenerate ordering: an optimistic local (seq 0n) leads the window.
          // getFirstSeq must skip it so the BEFORE cursor is the first SERVER seq
          // (5), not 0n -- which the backend resolves as an empty `seq < 0` page
          // that would permanently clear hasMoreOlder.
          store.setMessages('a1', [makeMessage('local-1', 0n), makeMessage('m5', 5n), makeMessage('m6', 6n)], true)
          mockListAgentMessages.mockResolvedValueOnce({ messages: [makeMessage('o1', 1n)], hasMore: false })
          await store.loadOlderMessages('w1', 'a1')
          expect(mockListAgentMessages).toHaveBeenCalledWith('w1', { agentId: 'a1', anchor: MessagePageAnchor.BEFORE, cursorSeq: 5n, limit: 50 })
          dispose()
        })
      })
    })

    describe('loadNewerPage (single page, scroll-down)', () => {
      it('appends a page, clears hasMoreNewer at the tail', async () => {
        await createRoot(async (dispose) => {
          const store = createChatStore()
          store.setMessages('a1', Array.from({ length: 50 }, (_, i) => makeMessage(`m${i}`, BigInt(i + 1))))
          store.trimNewestEnd('a1', 30) // window seq 1..30, hasMoreNewer=true
          expect(store.getLastSeq('a1')).toBe(30n)

          const newer = Array.from({ length: 20 }, (_, i) => makeMessage(`n${i}`, BigInt(i + 31)))
          mockListAgentMessages.mockResolvedValueOnce({ messages: newer, hasMore: false })

          await store.loadNewerPage('w1', 'a1')
          const msgs = store.getMessages('a1')
          expect(msgs).toHaveLength(50)
          expect(msgs.at(-1)!.seq).toBe(50n)
          expect(store.hasNewerMessages('a1')).toBe(false)
          expect(mockListAgentMessages).toHaveBeenLastCalledWith('w1', { agentId: 'a1', anchor: MessagePageAnchor.AFTER, cursorSeq: 30n, limit: 50 })
          dispose()
        })
      })

      it('replaces a stale in-window row when the page carries its id under a NEW seq (reseq)', async () => {
        await createRoot(async (dispose) => {
          const store = createChatStore()
          store.setMessages('a1', Array.from({ length: 50 }, (_, i) => makeMessage(`m${i}`, BigInt(i + 1))))
          store.trimNewestEnd('a1', 30) // window seq 1..30 (m0..m29), hasMoreNewer=true
          // m10 sits in the window at seq 11. The server consolidates it (notification
          // reseq -> MAX(seq)+1), so the scroll-down page carries the SAME id under a
          // new, higher seq. Seq-only dedup would miss it and insert a duplicate row
          // sharing one id -- which the id-keyed virtualizer collapses onto one slot.
          const reseqd = makeMessage('m10', 35n)
          const newer = [makeMessage('n31', 31n), reseqd]
          mockListAgentMessages.mockResolvedValueOnce({ messages: newer, hasMore: false })

          await store.loadNewerPage('w1', 'a1')
          const msgs = store.getMessages('a1')
          // Exactly one row carries id m10, and it is the reseq'd copy (seq 35), not
          // the stale seq-11 copy.
          const m10s = msgs.filter(m => m.id === 'm10')
          expect(m10s).toHaveLength(1)
          expect(m10s[0].seq).toBe(35n)
          // No id appears twice anywhere in the window.
          const ids = msgs.map(m => m.id)
          expect(new Set(ids).size).toBe(ids.length)
          // 30 in-window - 1 stale m10 + 2 appended (n31, reseq'd m10) = 31.
          expect(msgs).toHaveLength(31)
          // The reseq'd row is reinserted BY SEQ (35, after the seq-30 tail), not
          // merely appended -- the whole server window stays seq-ascending.
          const serverSeqs = msgs.map(m => m.seq).filter(s => s !== 0n)
          expect(serverSeqs).toEqual([...serverSeqs].sort((a, b) => (a < b ? -1 : a > b ? 1 : 0)))
          expect(serverSeqs.at(-1)).toBe(35n)
          dispose()
        })
      })

      it('replaces both the stale same-id copy AND the occupant when a reseq new seq collides with a DIFFERENT in-window row', async () => {
        await createRoot(async (dispose) => {
          const store = createChatStore()
          store.setMessages('a1', Array.from({ length: 50 }, (_, i) => makeMessage(`m${i}`, BigInt(i + 1))))
          store.trimNewestEnd('a1', 30) // window seq 1..30 (m0..m29), hasMoreNewer=true
          // Reseq m10 (seq 11) to seq 21 -- which COLLIDES with m20 (seq 21), still in
          // the window. The id-keyed test admits the reseq and drops its stale same-id
          // copy (m10@11); the "replace" rule also drops the stale OCCUPANT m20@21,
          // because the reseq'd row authoritatively owns seq 21 now (a real server
          // reseq to MAX(seq)+1 never collides -- a collision means the occupant is
          // stale and its true position arrives in a later fetch).
          const reseqd = makeMessage('m10', 21n)
          mockListAgentMessages.mockResolvedValueOnce({ messages: [makeMessage('n31', 31n), reseqd], hasMore: false })

          await store.loadNewerPage('w1', 'a1')
          const msgs = store.getMessages('a1')
          // m10 appears exactly once, at its reseq'd seq 21 (not the stale 11).
          const m10s = msgs.filter(m => m.id === 'm10')
          expect(m10s).toHaveLength(1)
          expect(m10s[0].seq).toBe(21n)
          // No id appears twice anywhere in the window.
          const ids = msgs.map(m => m.id)
          expect(new Set(ids).size).toBe(ids.length)
          // The stale occupant m20 is REPLACED (dropped) -- the newcomer owns seq 21,
          // so seq 21 appears exactly once and there is no duplicate seq.
          expect(msgs.some(m => m.id === 'm20')).toBe(false)
          expect(msgs.filter(m => m.seq === 21n)).toHaveLength(1)
          // 30 in-window - stale m10@11 - replaced m20@21 + (n31, reseq'd m10@21) = 30.
          expect(msgs).toHaveLength(30)
          // The window stays strictly seq-ascending (no duplicate seq), so the binary
          // searches over the offset map remain valid.
          const serverSeqs = msgs.map(m => m.seq).filter(s => s !== 0n)
          expect(serverSeqs).toEqual([...serverSeqs].sort((a, b) => (a < b ? -1 : a > b ? 1 : 0)))
          dispose()
        })
      })

      it('prunes the stale command stream when a reseq replaces a row under a new spanId', async () => {
        await createRoot(async (dispose) => {
          const store = createChatStore()
          const initial = Array.from({ length: 50 }, (_, i) => makeMessage(`m${i}`, BigInt(i + 1)))
          // m10 (seq 11) carries spanId 'spanX' and a live command stream.
          initial[10] = makeToolUseSpan('m10', 11n, 'spanX')
          store.setMessages('a1', initial)
          store.trimNewestEnd('a1', 30) // window seq 1..30, m10 in window
          store.appendCommandStream('a1', 'spanX', 'item/commandExecution/output', 'output')
          expect(store.getCommandStream('a1', 'spanX')).toHaveLength(1)

          // The page reseqs m10 to seq 35 under a DIFFERENT spanId ('spanY'), so the
          // stale m10 (spanX) is dropped and spanX is left unreferenced. The merge
          // must prune spanX's now-orphaned stream like every other structural drop.
          const reseqd = makeToolUseSpan('m10', 35n, 'spanY')
          mockListAgentMessages.mockResolvedValueOnce({ messages: [makeMessage('n31', 31n), reseqd], hasMore: false })
          await store.loadNewerPage('w1', 'a1')

          expect(store.getMessages('a1').filter(m => m.id === 'm10')).toHaveLength(1)
          expect(store.getMessages('a1').find(m => m.spanId === 'spanX')).toBeUndefined()
          // spanX's buffered stream is spared + recorded; the sweep reclaims it (vs
          // leaking for the session if the merge ignored the dropped row's span).
          expect(store.getCommandStream('a1', 'spanX')).toHaveLength(1)
          store.sweepOrphanedBufferedSpans('a1')
          expect(store.getCommandStream('a1', 'spanX')).toHaveLength(0)
          dispose()
        })
      })

      it('is a no-op when already at the tail (hasMoreNewer false)', async () => {
        await createRoot(async (dispose) => {
          mockListAgentMessages.mockClear()
          const store = createChatStore()
          store.setMessages('a1', [makeMessage('m1', 1n)])
          await store.loadNewerPage('w1', 'a1')
          expect(mockListAgentMessages).not.toHaveBeenCalled()
          dispose()
        })
      })

      it('re-anchors via jumpToLatest when the server range emptied but hasMoreNewer is set', async () => {
        await createRoot(async (dispose) => {
          mockListAgentMessages.mockClear()
          const store = createChatStore()
          store.setMessages('a1', [makeMessage('m1', 1n), makeMessage('m2', 2n)])
          store.trimNewestEnd('a1', 1) // window [m1], hasMoreNewer=true
          // A messageDeleted broadcast removes the last server message, leaving the
          // window with no server cursor (getLastSeq 0n) while hasMoreNewer stays set.
          store.removeMessage('a1', 'm1')
          expect(store.getLastSeq('a1')).toBe(0n)
          expect(store.hasNewerMessages('a1')).toBe(true)

          // Mirror loadOlderMessages' OLDEST fallback: with no AFTER cursor, page
          // down must re-anchor on a fresh LATEST page instead of wedging forever.
          mockListAgentMessages.mockResolvedValue({ messages: [makeMessage('m1', 1n), makeMessage('m2', 2n)], hasMore: false })
          await store.loadNewerPage('w1', 'a1')

          expect(mockListAgentMessages).toHaveBeenCalledWith('w1', { agentId: 'a1', anchor: MessagePageAnchor.LATEST, limit: 50 })
          expect(store.getMessages('a1').map(m => m.id)).toEqual(['m1', 'm2'])
          expect(store.hasNewerMessages('a1')).toBe(false) // back at the live tail
          dispose()
        })
      })

      it('trims the oldest end when the appended page overflows the ceiling', async () => {
        await createRoot(async (dispose) => {
          const store = createChatStore()
          const ceiling = MAX_LOADED_CHAT_MESSAGES_CEILING
          // A ceiling-full window (seq 1..ceiling) scrolled away from the tail.
          store.setMessages('a1', Array.from({ length: ceiling + 200 }, (_, i) => makeMessage(`m${i}`, BigInt(i + 1))))
          store.trimNewestEnd('a1', ceiling) // seq 1..ceiling, hasMoreNewer=true
          const newer = Array.from({ length: 50 }, (_, i) => makeMessage(`n${i}`, BigInt(ceiling + 1 + i)))
          mockListAgentMessages.mockResolvedValueOnce({ messages: newer, hasMore: true })

          await store.loadNewerPage('w1', 'a1')
          const msgs = store.getMessages('a1')
          expect(msgs).toHaveLength(ceiling)
          expect(msgs.at(-1)!.seq).toBe(BigInt(ceiling + 50)) // newest appended kept
          expect(msgs[0].seq).toBe(51n) // oldest end trimmed by 50
          expect(store.hasOlderMessages('a1')).toBe(true)
          expect(store.hasNewerMessages('a1')).toBe(true) // still more newer
          dispose()
        })
      })

      it('inserts the appended page before a trailing optimistic local (seq 0n)', async () => {
        await createRoot(async (dispose) => {
          const store = createChatStore()
          store.setMessages('a1', Array.from({ length: 50 }, (_, i) => makeMessage(`m${i}`, BigInt(i + 1))))
          store.trimNewestEnd('a1', 30) // window seq 1..30, hasMoreNewer=true
          // A failed/undelivered optimistic local sits at the tail.
          store.addMessage('a1', makeMessage('local-1', 0n))
          expect(store.getMessages('a1').at(-1)!.id).toBe('local-1')

          const newer = Array.from({ length: 10 }, (_, i) => makeMessage(`n${i}`, BigInt(i + 31)))
          mockListAgentMessages.mockResolvedValueOnce({ messages: newer, hasMore: false })

          await store.loadNewerPage('w1', 'a1')
          const msgs = store.getMessages('a1')
          // Server messages slot in before the local, which stays pinned to the
          // tail rather than being stranded mid-list.
          expect(msgs.at(-1)!.id).toBe('local-1')
          expect(msgs.at(-2)!.seq).toBe(40n)
          expect(msgs.filter(m => m.seq === 0n)).toHaveLength(1)
          dispose()
        })
      })

      it('reconciles a pending optimistic local when the page carries its server echo', async () => {
        await createRoot(async (dispose) => {
          const store = createChatStore()
          store.setMessages('a1', Array.from({ length: 50 }, (_, i) => makeMessage(`m${i}`, BigInt(i + 1))))
          store.trimNewestEnd('a1', 30) // window seq 1..30, hasMoreNewer=true
          // A successfully-sent message whose live broadcast the live-append guard
          // dropped (beyondTail while scrolled away) is still pending as a local.
          store.addMessage('a1', makeUserMessage('local-hello', 0n, 'hello'))
          expect(store.getMessages('a1').at(-1)!.id).toBe('local-hello')

          // The scroll-down page carries the server echo of that local (same
          // user-message signature, a real seq).
          const echo = makeUserMessage('server-hello', 35n, 'hello')
          const newer = [makeMessage('n31', 31n), echo, makeMessage('n36', 36n)]
          mockListAgentMessages.mockResolvedValueOnce({ messages: newer, hasMore: false })

          await store.loadNewerPage('w1', 'a1')
          const msgs = store.getMessages('a1')
          // The local is reconciled away (no duplicate bubble); only the server
          // copy survives, and no optimistic local lingers at the tail.
          expect(msgs.filter(m => m.id === 'local-hello')).toHaveLength(0)
          expect(msgs.some(m => m.id === 'server-hello')).toBe(true)
          expect(msgs.filter(m => m.seq === 0n)).toHaveLength(0)
          expect(msgs).toHaveLength(33) // 30 + three appended, minus the reconciled local
          dispose()
        })
      })

      it('reconciles a failed local to a coinciding server echo on scroll-down (delivery is truth)', async () => {
        await createRoot(async (dispose) => {
          const store = createChatStore()
          store.setMessages('a1', Array.from({ length: 50 }, (_, i) => makeMessage(`m${i}`, BigInt(i + 1))))
          store.trimNewestEnd('a1', 30) // window seq 1..30, hasMoreNewer=true
          // A send marked failed -- but the server echoes the same text, proving it WAS
          // delivered. Under "delivery is truth" the failed bubble reconciles to the echo
          // and its error annotation is reclaimed (no failed-AND-delivered pair).
          store.addMessage('a1', makeUserMessage('local-failed', 0n, 'hello', 'Failed to deliver'))
          expect(store.messageErrors()['local-failed']).toBe('Failed to deliver')

          const echo = makeUserMessage('server-hello', 35n, 'hello')
          mockListAgentMessages.mockResolvedValueOnce({ messages: [echo], hasMore: false })

          await store.loadNewerPage('w1', 'a1')
          const msgs = store.getMessages('a1')
          expect(msgs.some(m => m.id === 'local-failed')).toBe(false) // reconciled away
          expect(msgs.some(m => m.id === 'server-hello')).toBe(true)
          expect(store.messageErrors()['local-failed']).toBeUndefined() // annotation reclaimed
          dispose()
        })
      })

      it('keeps hasMoreNewer when a live message was dropped mid-fetch (latestLiveSeq beyond the page)', async () => {
        await createRoot(async (dispose) => {
          const store = createChatStore()
          store.setMessages('a1', Array.from({ length: 50 }, (_, i) => makeMessage(`m${i}`, BigInt(i + 1))))
          store.trimNewestEnd('a1', 30) // window seq 1..30, hasMoreNewer=true
          // A live message beyond the window arrives mid-scroll: the live-append
          // guard drops it but records its seq as the observed live tail.
          store.addMessage('a1', makeMessage('live60', 60n))
          expect(store.liveTail.get('a1')).toBe(60n)
          // The scroll-down page raced that broadcast: it reaches seq 50 and the
          // server reports has_more=false, yet seq 60 is not in it.
          const newer = Array.from({ length: 20 }, (_, i) => makeMessage(`n${i}`, BigInt(i + 31)))
          mockListAgentMessages.mockResolvedValueOnce({ messages: newer, hasMore: false })

          await store.loadNewerPage('w1', 'a1')
          // has_more=false does NOT prove we reached the LIVE tail: hasMoreNewer
          // stays set so the gap (seq 60) is still reachable rather than stranded
          // with the view claiming to be at the bottom.
          expect(store.getLastSeq('a1')).toBe(50n)
          expect(store.hasNewerMessages('a1')).toBe(true)
          dispose()
        })
      })

      it('clears hasMoreNewer (clamping latestLiveSeq) when a dedup-stall reaches the server tail', async () => {
        await createRoot(async (dispose) => {
          const store = createChatStore()
          store.setMessages('a1', Array.from({ length: 50 }, (_, i) => makeMessage(`m${i}`, BigInt(i + 1))))
          store.trimNewestEnd('a1', 30) // window seq 1..30, hasMoreNewer=true
          // A live message bumped latestLiveSeq beyond the real tail and was then
          // deleted server-side, so no forward page can ever reach it.
          store.addMessage('a1', makeMessage('live60', 60n))
          expect(store.liveTail.get('a1')).toBe(60n)
          // The scroll-down page reaches the genuine server tail (has_more=false)
          // and brings nothing new (seq 60 is gone): getLastSeq stays at 30.
          mockListAgentMessages.mockResolvedValueOnce({ messages: [], hasMore: false })

          await store.loadNewerPage('w1', 'a1')
          // Rather than wedging hasMoreNewer on forever chasing the vanished seq 60
          // (which would keep the scroll-to-bottom button lit and hide the
          // streaming tail), latestLiveSeq is clamped to the real tail so the view
          // settles at the bottom.
          expect(store.getLastSeq('a1')).toBe(30n)
          expect(store.liveTail.get('a1')).toBe(30n)
          expect(store.hasNewerMessages('a1')).toBe(false)
          dispose()
        })
      })

      it('keeps a message broadcast mid-fetch reachable rather than clamping it away with the dedup-stall', async () => {
        await createRoot(async (dispose) => {
          const store = createChatStore()
          store.setMessages('a1', Array.from({ length: 50 }, (_, i) => makeMessage(`m${i}`, BigInt(i + 1))))
          store.trimNewestEnd('a1', 30) // window seq 1..30, hasMoreNewer=true, latestLiveSeq=50
          expect(store.liveTail.get('a1')).toBe(50n)
          // The recorded-but-unreached seqs (31..50) have vanished server-side, so
          // the forward page returns empty with has_more=false (a dedup-stall).
          // But WHILE that fetch is in flight, a genuinely-new message (seq 70) is
          // broadcast: the live-append guard drops it from the scrolled-away window
          // yet records its seq as the live tail (latestLiveSeq advances past the
          // entry snapshot of 50).
          mockListAgentMessages.mockImplementationOnce(async () => {
            store.addMessage('a1', makeMessage('live70', 70n))
            return { messages: [], hasMore: false }
          })

          await store.loadNewerPage('w1', 'a1')
          // The clamp must NOT discard seq 70: it is genuinely reachable (it arrived
          // after entry, so it isn't the vanished gap the clamp settles). Stranding
          // it would clear hasMoreNewer with the view falsely claiming to be at the
          // tail and the scroll-to-bottom affordance hidden. hasMoreNewer stays set
          // so a further scroll-down (or jump) pulls seq 70.
          expect(store.getLastSeq('a1')).toBe(30n)
          expect(store.liveTail.get('a1')).toBe(70n)
          expect(store.hasNewerMessages('a1')).toBe(true)
          dispose()
        })
      })

      it('does NOT clamp the recorded tail when a concurrent delete SHRINKS the window mid-fetch', async () => {
        await createRoot(async (dispose) => {
          const store = createChatStore()
          store.setMessages('a1', Array.from({ length: 50 }, (_, i) => makeMessage(`m${i}`, BigInt(i + 1))))
          store.trimNewestEnd('a1', 30) // window seq 1..30, hasMoreNewer=true, latestLiveSeq=50
          expect(store.liveTail.get('a1')).toBe(50n)
          // The forward page reaches the server tail with nothing new. Normally that is a
          // dedup-stall that clamps latestLiveSeq to the window. But WHILE the fetch is in
          // flight a messageDeleted removes the window's TAIL row (seq 30, id m29) -- NOT
          // the recorded tail. That lowers getLastSeq BELOW the pre-fetch cursor: a window
          // SHRINK, not a dedup-stall. The still-reachable recorded tail (31..50) must be
          // preserved, not erased -- the delete is reconciled by onDelete + a later fill.
          mockListAgentMessages.mockImplementationOnce(async () => {
            store.removeMessage('a1', 'm29') // seq 30
            return { messages: [], hasMore: false }
          })

          await store.loadNewerPage('w1', 'a1')
          expect(store.getLastSeq('a1')).toBe(29n) // window shrank to seq 1..29
          expect(store.liveTail.get('a1')).toBe(50n) // recorded tail preserved (NOT clamped to 29)
          expect(store.hasNewerMessages('a1')).toBe(true) // more still below
          dispose()
        })
      })
    })

    describe('addMessage live-append guard', () => {
      it('drops a genuinely-new tail message while scrolled away, recording latestLiveSeq', () => {
        createRoot((dispose) => {
          const store = createChatStore()
          store.setMessages('a1', Array.from({ length: 200 }, (_, i) => makeMessage(`m${i}`, BigInt(i + 1))))
          store.trimNewestEnd('a1', 150) // window seq 1..150, hasMoreNewer=true
          expect(store.getLastSeq('a1')).toBe(150n)

          store.addMessage('a1', makeMessage('live201', 201n))
          expect(store.getMessages('a1')).toHaveLength(150) // dropped
          expect(store.liveTail.get('a1')).toBe(201n) // but recorded
          dispose()
        })
      })

      it('during catch-up drops a non-contiguous beyond-tail frame (recording the live tail) but splices a contiguous one', () => {
        createRoot((dispose) => {
          const store = createChatStore()
          store.setMessages('a1', [makeMessage('m1', 1n)]) // at tail, lastSeq=1, hasMoreNewer=false
          store.setCatchingUp('a1', true)
          // seq 3 is more than one past the loaded tail (1): the bounded replay hasn't
          // filled seq 2 yet, so splicing would tear a hole. Dropped + recorded in
          // liveTail; the continuous reconcile forward-fills (1, 3] contiguously. This is
          // robust even when the worker's tail is indeterminate (liveTail can't be raised).
          store.addMessage('a1', makeMessage('m3', 3n))
          expect(store.getMessages('a1').find(m => m.id === 'm3')).toBeUndefined()
          expect(store.liveTail.get('a1')).toBe(3n)
          expect(store.caughtUpToLiveTail('a1')).toBe(false)
          // The next in-order replay page (contiguous, seq 2) IS spliced.
          store.addMessage('a1', makeMessage('m2', 2n))
          expect(store.getMessages('a1').map(m => m.id)).toEqual(['m1', 'm2'])
          dispose()
        })
      })

      it('in the LIVE phase splices a non-contiguous beyond-tail frame (a delete-gap, not a catch-up gap)', () => {
        createRoot((dispose) => {
          const store = createChatStore()
          store.setMessages('a1', [makeMessage('m1', 1n)]) // live phase (catchingUp false), lastSeq=1
          // A frame at seq 3 (e.g. seq 2 was a failed message since deleted): in the LIVE
          // phase the recorded-live-tail comparison keeps it (recordedLiveTail <= lastSeq),
          // so it SPLICES rather than drop+refetch -- the opposite of the catch-up phase.
          store.addMessage('a1', makeMessage('m3', 3n))
          expect(store.getMessages('a1').map(m => m.id)).toEqual(['m1', 'm3'])
          dispose()
        })
      })

      it('seeds (does NOT drop) a live message into an empty server window even with hasMoreNewer', async () => {
        await createRoot(async (dispose) => {
          const store = createChatStore()
          // Reach an empty server window that still flags hasMoreNewer (the
          // symmetric counterpart of the beforeHead/firstSeq guard). A jump-to-
          // oldest whose page came back empty but with more beyond it lands here.
          mockListAgentMessages.mockResolvedValueOnce({ messages: [], hasMore: true })
          await store.jumpToOldestMessages('w1', 'a1')
          expect(store.getMessages('a1')).toHaveLength(0)
          expect(store.hasNewerMessages('a1')).toBe(true)
          expect(store.getLastSeq('a1')).toBe(0n)

          // With lastSeq 0n there is no loaded range to tear a gap against, so the
          // message must seed the window rather than be swallowed by `seq > 0n`.
          store.addMessage('a1', makeMessage('live5', 5n))
          expect(store.getMessages('a1').map(m => m.id)).toEqual(['live5'])
          dispose()
        })
      })

      it('still applies an in-place update to an already-windowed message', () => {
        createRoot((dispose) => {
          const store = createChatStore()
          store.setMessages('a1', Array.from({ length: 200 }, (_, i) => makeMessage(`m${i}`, BigInt(i + 1))))
          store.trimNewestEnd('a1', 150) // window seq 1..150
          // m74 has seq 75 and is inside the window; re-send it (same id+seq).
          const updated = makeUserMessage('m74', 75n, 'edited')
          store.addMessage('a1', updated)
          const msgs = store.getMessages('a1')
          expect(msgs).toHaveLength(150)
          // Updated in place (content reflects the edit), not dropped.
          const m74 = msgs.find(m => m.id === 'm74')!
          expect(new TextDecoder().decode(m74.content)).toContain('edited')
          dispose()
        })
      })

      it('bumps the content version on a same-seq in-place update', () => {
        createRoot((dispose) => {
          const store = createChatStore()
          store.setMessages('a1', Array.from({ length: 3 }, (_, i) => makeMessage(`m${i}`, BigInt(i + 1))))
          // m1 has seq 2; no in-place update yet, so its content version is 0.
          expect(store.getMessageContentVersion('m1')).toBe(0)

          // Re-send m1 (same id+seq) with new content: a same-seq in-place merge,
          // which preserves the proxy/seq -- the version is the only signal a
          // consumer (the classified-entry cache / height estimate) can key on.
          store.addMessage('a1', makeUserMessage('m1', 2n, 'edited'))
          expect(store.getMessageContentVersion('m1')).toBe(1)
          store.addMessage('a1', makeUserMessage('m1', 2n, 'edited again'))
          expect(store.getMessageContentVersion('m1')).toBe(2)
          // An untouched row keeps version 0.
          expect(store.getMessageContentVersion('m0')).toBe(0)
          dispose()
        })
      })

      it('resolves a tool_result\'s opener content version by spanId', () => {
        createRoot((dispose) => {
          const store = createChatStore()
          // An opener (tool_use) and its result (tool_result) share one spanId. The
          // result sizes its diff from the OPENER, so it must be able to observe the
          // opener's content version (which an in-place opener edit bumps).
          store.setMessages('a1', [makeToolUseSpan('op', 1n, 'spanX'), makeToolResultSpan('res', 2n, 'spanX')])
          expect(store.getToolUseContentVersionBySpanId('a1', 'spanX')).toBe(0)
          // The opener's body is replaced in place with NEW content (same id+seq, edited
          // tool input) -> its version bumps, and the result's spanId lookup reflects it.
          store.addMessage('a1', makeToolUseSpan('op', 1n, 'spanX', 0n, { file_path: '/edited' }))
          expect(store.getToolUseContentVersionBySpanId('a1', 'spanX')).toBe(1)
          // An unindexed span has no opener -> 0.
          expect(store.getToolUseContentVersionBySpanId('a1', 'nope')).toBe(0)
          dispose()
        })
      })

      it('reclaims a removed message\'s content version (no per-session leak)', () => {
        createRoot((dispose) => {
          const store = createChatStore()
          store.setMessages('a1', [makeUserMessage('m1', 1n, 'first')])
          store.addMessage('a1', makeUserMessage('m1', 1n, 'edited')) // in-place merge -> version 1
          expect(store.getMessageContentVersion('m1')).toBe(1)

          store.removeMessage('a1', 'm1')
          // The row is gone; its version entry must not linger for the session.
          expect(store.getMessageContentVersion('m1')).toBe(0)
          dispose()
        })
      })

      it('reclaims content versions of rows trimmed off the oldest end, sparing kept rows', () => {
        createRoot((dispose) => {
          const store = createChatStore()
          store.setMessages('a1', Array.from({ length: 5 }, (_, i) => makeMessage(`m${i}`, BigInt(i + 1))))
          // Edit m0 (oldest, will be dropped) and m2 (kept) so both carry versions.
          store.addMessage('a1', makeUserMessage('m0', 1n, 'edited'))
          store.addMessage('a1', makeUserMessage('m2', 3n, 'edited'))
          expect(store.getMessageContentVersion('m0')).toBe(1)
          expect(store.getMessageContentVersion('m2')).toBe(1)

          store.trimOldestEnd('a1', 3) // keep newest 3 (m2..m4), drop m0/m1
          expect(store.getMessages('a1').map(m => m.id)).toEqual(['m2', 'm3', 'm4'])
          expect(store.getMessageContentVersion('m0')).toBe(0) // dropped -> reclaimed
          expect(store.getMessageContentVersion('m2')).toBe(1) // kept -> preserved
          dispose()
        })
      })

      it('reclaims content versions of rows dropped by a full-window replace, sparing kept rows', () => {
        createRoot((dispose) => {
          const store = createChatStore()
          store.setMessages('a1', Array.from({ length: 3 }, (_, i) => makeMessage(`m${i}`, BigInt(i + 1))))
          // Bump m0 (will be dropped) and m1 (will be kept by id across the replace).
          store.addMessage('a1', makeUserMessage('m0', 1n, 'edited'))
          store.addMessage('a1', makeUserMessage('m1', 2n, 'edited'))
          expect(store.getMessageContentVersion('m0')).toBe(1)
          expect(store.getMessageContentVersion('m1')).toBe(1)

          // A full-window replace (jump-to-latest / reconnect snapshot / setMessages)
          // drops m0 and m2 but re-lands m1. Without reclamation here the un-capped
          // messageContentVersions map would orphan m0's counter for the session.
          store.setMessages('a1', [makeMessage('m1', 2n), makeMessage('m9', 9n)])
          expect(store.getMessageContentVersion('m0')).toBe(0) // dropped -> reclaimed
          expect(store.getMessageContentVersion('m1')).toBe(1) // re-landed -> preserved
          dispose()
        })
      })

      it('reclaims content versions of rows trimmed off the newest end', () => {
        createRoot((dispose) => {
          const store = createChatStore()
          store.setMessages('a1', Array.from({ length: 5 }, (_, i) => makeMessage(`m${i}`, BigInt(i + 1))))
          store.addMessage('a1', makeUserMessage('m4', 5n, 'edited')) // newest, will be dropped
          store.addMessage('a1', makeUserMessage('m0', 1n, 'edited')) // oldest, kept
          expect(store.getMessageContentVersion('m4')).toBe(1)

          store.trimNewestEnd('a1', 3) // keep oldest 3 (m0..m2), drop m3/m4
          expect(store.getMessages('a1').map(m => m.id)).toEqual(['m0', 'm1', 'm2'])
          expect(store.getMessageContentVersion('m4')).toBe(0) // dropped -> reclaimed
          expect(store.getMessageContentVersion('m0')).toBe(1) // kept -> preserved
          dispose()
        })
      })

      it('reclaims error annotations of rows trimmed off the oldest end, sparing kept rows', () => {
        createRoot((dispose) => {
          const store = createChatStore()
          // m0 (oldest, dropped) and m2 (kept) are failed sends carrying a
          // persisted delivery_error, so both get an error annotation on load.
          const msgs = Array.from({ length: 5 }, (_, i) =>
            makeMessage(`m${i}`, BigInt(i + 1), i === 0 || i === 2 ? `err${i}` : ''))
          store.setMessages('a1', msgs)
          expect(store.messageErrors().m0).toBe('err0')
          expect(store.messageErrors().m2).toBe('err2')

          store.trimOldestEnd('a1', 3) // keep newest 3 (m2..m4), drop m0/m1
          expect(store.messageErrors().m0).toBeUndefined() // dropped -> reclaimed
          expect(store.messageErrors().m2).toBe('err2') // kept -> preserved
          dispose()
        })
      })

      it('reclaims error annotations of rows trimmed off the newest end, sparing kept rows', () => {
        createRoot((dispose) => {
          const store = createChatStore()
          // m0 (oldest, kept) and m4 (newest, dropped) carry delivery errors.
          const msgs = Array.from({ length: 5 }, (_, i) =>
            makeMessage(`m${i}`, BigInt(i + 1), i === 0 || i === 4 ? `err${i}` : ''))
          store.setMessages('a1', msgs)
          expect(store.messageErrors().m4).toBe('err4')

          store.trimNewestEnd('a1', 3) // keep oldest 3 (m0..m2), drop m3/m4
          expect(store.messageErrors().m4).toBeUndefined() // dropped -> reclaimed
          expect(store.messageErrors().m0).toBe('err0') // kept -> preserved
          dispose()
        })
      })

      it('reclaims error annotations of rows dropped by a full-window replace, sparing kept rows', () => {
        createRoot((dispose) => {
          const store = createChatStore()
          store.setMessages('a1', [makeMessage('m0', 1n, 'err0'), makeMessage('m1', 2n, 'err1')])
          expect(store.messageErrors().m0).toBe('err0')

          // A full-window replace (jump-to-latest / reconnect snapshot) drops m0
          // but re-lands m1 (which re-carries its error). Without reclamation here
          // m0's error annotation would orphan in the un-capped errors map.
          store.setMessages('a1', [makeMessage('m1', 2n, 'err1'), makeMessage('m9', 9n)])
          expect(store.messageErrors().m0).toBeUndefined() // dropped -> reclaimed
          expect(store.messageErrors().m1).toBe('err1') // re-landed -> preserved
          dispose()
        })
      })

      it('reclaims a removed message\'s error annotation and pending label', () => {
        createRoot((dispose) => {
          const store = createChatStore()
          store.setMessages('a1', [makeMessage('m1', 1n, 'boom')])
          store.setMessagePendingLabel('m1', 'queued')
          expect(store.messageErrors().m1).toBe('boom')
          expect(store.messagePendingLabels().m1).toBe('queued')

          store.removeMessage('a1', 'm1')
          // Both per-id annotations must be reclaimed, not just the content version.
          expect(store.messageErrors().m1).toBeUndefined()
          expect(store.messagePendingLabels().m1).toBeUndefined()
          dispose()
        })
      })

      it('reclaims a pending label of a row trimmed off the oldest end', () => {
        createRoot((dispose) => {
          const store = createChatStore()
          store.setMessages('a1', Array.from({ length: 5 }, (_, i) => makeMessage(`m${i}`, BigInt(i + 1))))
          store.setMessagePendingLabel('m0', 'queued') // oldest, will be dropped
          store.setMessagePendingLabel('m4', 'queued') // newest, kept
          expect(store.messagePendingLabels().m0).toBe('queued')

          store.trimOldestEnd('a1', 3) // keep newest 3 (m2..m4), drop m0/m1
          expect(store.messagePendingLabels().m0).toBeUndefined() // dropped -> reclaimed
          expect(store.messagePendingLabels().m4).toBe('queued') // kept -> preserved
          dispose()
        })
      })

      it('flows fresh content to the classified-entry cache on a same-seq in-place update', () => {
        createRoot((dispose) => {
          // End-to-end: the store reuses the proxy on a same-seq merge, so the
          // classified-entry cache (which keys freshness on seq) would render the
          // pre-update body unless the store both bumps the content version AND evicts
          // the by-reference parse cache. Wire a real cache to the store and prove it.
          const store = createChatStore()
          store.setMessages('a1', [makeUserMessage('m1', 1n, 'first')])
          const cache = createClassifiedEntryCache({
            messages: () => store.getMessages('a1'),
            contentVersionById: id => store.getMessageContentVersion(id),
            hasNewerMessages: () => false,
            showHiddenMessages: () => false,
          })
          const before = cache.visibleEntries()[0]
          expect(JSON.stringify(before.parsed.parentObject)).toContain('first')

          // Re-broadcast m1 with the SAME id+seq but new content: an in-place merge.
          store.addMessage('a1', makeUserMessage('m1', 1n, 'second'))
          const after = cache.visibleEntries()[0]
          expect(after).not.toBe(before) // rebuilt, not the stale cached ref
          expect(JSON.stringify(after.parsed.parentObject)).toContain('second')
          expect(JSON.stringify(after.parsed.parentObject)).not.toContain('first')
          dispose()
        })
      })

      it('still appends an optimistic local message (seq 0n)', () => {
        createRoot((dispose) => {
          const store = createChatStore()
          store.setMessages('a1', Array.from({ length: 200 }, (_, i) => makeMessage(`m${i}`, BigInt(i + 1))))
          store.trimNewestEnd('a1', 150)
          store.addMessage('a1', makeMessage('local-1', 0n))
          const msgs = store.getMessages('a1')
          expect(msgs).toHaveLength(151)
          expect(msgs.at(-1)!.id).toBe('local-1')
          dispose()
        })
      })

      it('inserts an in-range gap-fill but drops messages outside the loaded seq range', () => {
        createRoot((dispose) => {
          const store = createChatStore()
          // A windowed-away MIDDLE slice (so firstSeq > the global minimum) with a
          // gap at seq 175: seqs 100..174 then 176..300.
          const seqs = [
            ...Array.from({ length: 75 }, (_, i) => BigInt(100 + i)), // 100..174
            ...Array.from({ length: 125 }, (_, i) => BigInt(176 + i)), // 176..300
          ]
          store.setMessages('a1', seqs.map(s => makeMessage(`m${s}`, s)), true) // hasMoreOlder=true
          store.trimNewestEnd('a1', 150) // keep [100..174, 176..250]; hasMoreNewer=true
          expect(store.getFirstSeq('a1')).toBe(100n)
          expect(store.getLastSeq('a1')).toBe(250n)

          // In-range gap-fill: seq 175 sits inside [100, 250] and is missing, so
          // it is spliced into its ordered position rather than dropped.
          store.addMessage('a1', makeMessage('gap175', 175n))
          const afterGap = store.getMessages('a1')
          const idx = afterGap.findIndex(m => m.seq === 175n)
          expect(idx).toBeGreaterThan(0)
          expect(afterGap[idx - 1].seq).toBe(174n)
          expect(afterGap[idx + 1].seq).toBe(176n)

          // Older than the window's first server message (a re-broadcast of a row
          // trimmed away earlier): dropped, not prepended out of context.
          store.addMessage('a1', makeMessage('below99', 99n))
          expect(store.getMessages('a1').find(m => m.id === 'below99')).toBeUndefined()

          // Newer than the tail: dropped (recovered via forward-fetch / jump).
          store.addMessage('a1', makeMessage('above251', 251n))
          expect(store.getMessages('a1').find(m => m.id === 'above251')).toBeUndefined()
          dispose()
        })
      })

      it('drops a connect-time replay of an old message in front of a latest-page window (no gap)', () => {
        createRoot((dispose) => {
          const store = createChatStore()
          // A freshly-loaded LATEST page (seqs 1213..1262), with older history
          // still unloaded (hasMoreOlder) and at the tail (hasMoreNewer false).
          const seqs = Array.from({ length: 50 }, (_, i) => BigInt(1213 + i))
          store.setMessages('a1', seqs.map(s => makeMessage(`m${s}`, s)), true)
          expect(store.getFirstSeq('a1')).toBe(1213n)
          expect(store.hasOlderMessages('a1')).toBe(true)
          expect(store.hasNewerMessages('a1')).toBe(false)

          // A WatchEvents replay of the OLDEST message (seq 1) arrives via the
          // live path. It is below firstSeq with older history unloaded, so
          // splicing it in would tear a gap into the window -> dropped. (The old
          // guard only ran when hasMoreNewer, so it let this through.)
          store.addMessage('a1', makeMessage('replay1', 1n))
          const msgs = store.getMessages('a1')
          expect(msgs.find(m => m.id === 'replay1')).toBeUndefined()
          expect(msgs).toHaveLength(50)
          expect(store.getFirstSeq('a1')).toBe(1213n) // head unchanged -> no gap

          // A normal live append at the tail still works (not over-dropped).
          store.addMessage('a1', makeMessage('live1263', 1263n))
          expect(store.getMessages('a1').find(m => m.id === 'live1263')).toBeDefined()
          expect(store.getLastSeq('a1')).toBe(1263n)
          dispose()
        })
      })

      it('drops an in-place reseq that moves a windowed row beyond the live tail while scrolled away', () => {
        createRoot((dispose) => {
          const store = createChatStore()
          store.setMessages('a1', Array.from({ length: 50 }, (_, i) => makeMessage(`m${i}`, BigInt(i + 1))))
          store.trimNewestEnd('a1', 30) // window seq 1..30, hasMoreNewer=true, latestLiveSeq=50
          expect(store.getLastSeq('a1')).toBe(30n)
          expect(store.hasNewerMessages('a1')).toBe(true)

          // A notification already in the window (m4 at seq 5) is consolidated on
          // the backend and reseq'd to a brand-new tail seq beyond the loaded
          // window. Reinserting it at seq 60 would tear a [31..60) gap AND advance
          // getLastSeq to 60, making caughtUpToLiveTail trivially true while
          // 31..50 stay unloaded (the forward-fetch cursor would skip the gap).
          store.addMessage('a1', makeReseq('m4', 60n, 5n))

          // The moved row is dropped from its old position; latestLiveSeq records
          // the new tail; getLastSeq stays at the window tail; not caught up.
          expect(store.getMessages('a1').find(m => m.id === 'm4')).toBeUndefined()
          expect(store.getMessages('a1')).toHaveLength(29)
          expect(store.getLastSeq('a1')).toBe(30n)
          expect(store.liveTail.get('a1')).toBe(60n)
          expect(store.caughtUpToLiveTail('a1')).toBe(false)
          dispose()
        })
      })

      it('still reseq-reinserts an in-place update at the tail when at the live tail', () => {
        createRoot((dispose) => {
          const store = createChatStore()
          // At the live tail (hasMoreNewer false): a notification consolidation
          // reseq still reinserts in order, the normal behavior.
          store.addMessage('a1', makeMessage('notif', 1n))
          store.addMessage('a1', makeMessage('m2', 2n))
          store.addMessage('a1', makeReseq('notif', 3n, 1n))
          const msgs = store.getMessages('a1')
          expect(msgs.map(m => m.id)).toEqual(['m2', 'notif'])
          expect(store.getLastSeq('a1')).toBe(3n)
          dispose()
        })
      })

      it('reclaims the content version and error of a row reseq\'d beyond the window', () => {
        createRoot((dispose) => {
          const store = createChatStore()
          store.setMessages('a1', Array.from({ length: 50 }, (_, i) => makeMessage(`m${i}`, BigInt(i + 1))))
          store.trimNewestEnd('a1', 30) // window seq 1..30, hasMoreNewer=true, latestLiveSeq=50
          // A windowed notification (m4, seq 5) gets an in-place same-seq update with
          // NEW content (bumps its content version) and a delivery error -- the
          // precondition a reseq'd notification realistically carries before it
          // consolidates.
          store.addMessage('a1', makeUserMessage('m4', 5n, 'edited'))
          store.setMessageError('m4', 'transient')
          expect(store.getMessageContentVersion('m4')).toBe(1)
          expect(store.messageErrors().m4).toBe('transient')

          // It consolidates and reseqs to a tail seq beyond the loaded window, so the
          // moved row is dropped. Unlike the trim/delete/replace paths, this drop must
          // ALSO reclaim the counter + error or they leak for the session.
          store.addMessage('a1', makeReseq('m4', 60n, 5n))
          expect(store.getMessages('a1').find(m => m.id === 'm4')).toBeUndefined()
          expect(store.getMessageContentVersion('m4')).toBe(0) // reclaimed
          expect(store.messageErrors().m4).toBeUndefined() // cleared
          dispose()
        })
      })
    })

    describe('getResumeAfterSeq', () => {
      it('returns the window tail when caught up to the live tail', () => {
        createRoot((dispose) => {
          const store = createChatStore()
          store.setMessages('a1', [makeMessage('m1', 1n), makeMessage('m2', 2n)])
          expect(store.getResumeAfterSeq('a1')).toBe(2n)
          dispose()
        })
      })

      it('returns the live tail (not the lagging window tail) while scrolled away', () => {
        createRoot((dispose) => {
          const store = createChatStore()
          store.setMessages('a1', Array.from({ length: 50 }, (_, i) => makeMessage(`m${i}`, BigInt(i + 1))))
          store.trimNewestEnd('a1', 30) // window tail 30, latestLiveSeq 50
          // Resuming a WatchEvents subscription from the window tail (30) would
          // make the worker replay 31..50 -- all dropped by the live-append guard.
          // Resume from the live tail so only genuinely-new messages replay.
          expect(store.getLastSeq('a1')).toBe(30n)
          expect(store.getResumeAfterSeq('a1')).toBe(50n)
          dispose()
        })
      })

      it('returns 0n for an unknown agent', () => {
        createRoot((dispose) => {
          const store = createChatStore()
          expect(store.getResumeAfterSeq('nope')).toBe(0n)
          dispose()
        })
      })
    })

    describe('live tail recompute on delete', () => {
      it('lowers latestLiveSeq when the deleted row WAS the recorded live tail', () => {
        createRoot((dispose) => {
          const store = createChatStore()
          store.addMessage('a1', makeMessage('m1', 1n))
          store.addMessage('a1', makeMessage('m2', 2n))
          store.addMessage('a1', makeMessage('m3', 3n))
          expect(store.liveTail.get('a1')).toBe(3n)
          expect(store.caughtUpToLiveTail('a1')).toBe(true)

          // Delete the tail row (seq 3 == latestLiveSeq): the live tail drops to 2,
          // so caughtUpToLiveTail isn't left stuck behind a tail that's now gone.
          store.removeMessage('a1', 'm3')
          expect(store.getLastSeq('a1')).toBe(2n)
          expect(store.liveTail.get('a1')).toBe(2n)
          expect(store.caughtUpToLiveTail('a1')).toBe(true)
          dispose()
        })
      })

      it('clamps latestLiveSeq at the loaded tail when the deleted-tail broadcast carries a lagging newLatestSeq', () => {
        createRoot((dispose) => {
          const store = createChatStore()
          store.addMessage('a1', makeMessage('m1', 1n))
          store.addMessage('a1', makeMessage('m2', 2n))
          store.addMessage('a1', makeMessage('m3', 3n))
          expect(store.liveTail.get('a1')).toBe(3n)

          // The loaded tail (seq 3 == latestLiveSeq) is deleted, but the worker's
          // post-delete MAX(seq) read raced a concurrent insert (or hit the degraded
          // deletedSeq-1 error path) and reports a newLatestSeq (1) BELOW the row still
          // loaded at seq 2. Clamping keeps latestLiveSeq at the loaded tail (2) instead
          // of dropping it to 1 below a row the window still holds.
          store.removeMessage('a1', 'm3', 3n, 1n)
          expect(store.getLastSeq('a1')).toBe(2n)
          expect(store.liveTail.get('a1')).toBe(2n)
          dispose()
        })
      })

      it('leaves latestLiveSeq alone when a NON-tail row is deleted', () => {
        createRoot((dispose) => {
          const store = createChatStore()
          store.addMessage('a1', makeMessage('m1', 1n))
          store.addMessage('a1', makeMessage('m2', 2n))
          store.addMessage('a1', makeMessage('m3', 3n))
          store.removeMessage('a1', 'm2') // a middle row, not the tail
          expect(store.liveTail.get('a1')).toBe(3n)
          dispose()
        })
      })

      it('sets latestLiveSeq to the authoritative new tail when an UNLOADED beyond-window tail is deleted', () => {
        createRoot((dispose) => {
          const store = createChatStore()
          store.setMessages('a1', Array.from({ length: 50 }, (_, i) => makeMessage(`m${i}`, BigInt(i + 1))))
          store.trimNewestEnd('a1', 30) // window seq 1..30, hasMoreNewer=true, latestLiveSeq=50
          store.addMessage('a1', makeMessage('live60', 60n)) // observed but dropped beyond window
          expect(store.liveTail.get('a1')).toBe(60n)
          expect(store.caughtUpToLiveTail('a1')).toBe(false)

          // The beyond-window tail (seq 60) is deleted server-side. Its row isn't
          // loaded (removed is undefined), but the broadcast carries seq 60 == the
          // recorded tail AND the authoritative new tail (55) the worker computed
          // post-delete. latestLiveSeq is set to exactly 55 -- no deletedSeq-1 guess.
          store.removeMessage('a1', 'live60', 60n, 55n)
          expect(store.liveTail.get('a1')).toBe(55n)
          expect(store.caughtUpToLiveTail('a1')).toBe(false)
          dispose()
        })
      })

      it('falls back to deletedSeq-1 (clamped at the loaded tail) when no authoritative tail is carried', () => {
        createRoot((dispose) => {
          const store = createChatStore()
          store.setMessages('a1', Array.from({ length: 50 }, (_, i) => makeMessage(`m${i}`, BigInt(i + 1))))
          store.trimNewestEnd('a1', 30) // window seq 1..30, latestLiveSeq=50
          store.addMessage('a1', makeMessage('live60', 60n))
          expect(store.liveTail.get('a1')).toBe(60n)

          // No newLatestSeq arg (e.g. a legacy/local path): degrade to the
          // conservative deletedSeq-1 = 59, clamped at the loaded tail (30).
          store.removeMessage('a1', 'live60', 60n)
          expect(store.liveTail.get('a1')).toBe(59n)
          dispose()
        })
      })

      it('clamps an authoritative new tail at the loaded tail (never claims a tail below the window)', () => {
        createRoot((dispose) => {
          const store = createChatStore()
          store.setMessages('a1', Array.from({ length: 50 }, (_, i) => makeMessage(`m${i}`, BigInt(i + 1))))
          store.trimNewestEnd('a1', 30) // window seq 1..30, latestLiveSeq=50
          store.addMessage('a1', makeMessage('live60', 60n))

          // A (defensively low) authoritative tail of 10 -- below the loaded tail 30
          // -- is clamped up to 30 so the high-water can't drop beneath the window.
          store.removeMessage('a1', 'live60', 60n, 10n)
          expect(store.liveTail.get('a1')).toBe(30n)
          dispose()
        })
      })

      it('leaves latestLiveSeq alone when an unloaded NON-tail row is deleted (seq below the recorded tail)', () => {
        createRoot((dispose) => {
          const store = createChatStore()
          store.setMessages('a1', Array.from({ length: 50 }, (_, i) => makeMessage(`m${i}`, BigInt(i + 1))))
          store.trimNewestEnd('a1', 30) // window seq 1..30, latestLiveSeq=50
          store.addMessage('a1', makeMessage('live60', 60n)) // recorded tail = 60
          expect(store.liveTail.get('a1')).toBe(60n)

          // A delete arrives for an unloaded row at seq 40 -- below the recorded
          // tail (60), so the recorded tail (a different row) still exists. The
          // high-water must NOT move.
          store.removeMessage('a1', 'unloaded40', 40n)
          expect(store.liveTail.get('a1')).toBe(60n)
          dispose()
        })
      })

      it('clears a vanished live tail when jump-to-latest finds the server empty', async () => {
        await createRoot(async (dispose) => {
          const store = createChatStore()
          store.setMessages('a1', Array.from({ length: 50 }, (_, i) => makeMessage(`m${i}`, BigInt(i + 1))))
          store.trimNewestEnd('a1', 30) // window seq 1..30, hasMoreNewer=true, latestLiveSeq=50
          // A live message beyond the window was observed (raising latestLiveSeq),
          // then the whole history is deleted server-side while we're scrolled away.
          // If the messageDeleted broadcast is MISSED (e.g. a disconnect spanning the
          // delete), latestLiveSeq is left pointing at a tail that no longer exists,
          // and jump-to-latest is the backstop. (When the broadcast IS received,
          // removeMessage lowers the high-water directly -- covered above.)
          store.addMessage('a1', makeMessage('live60', 60n)) // dropped beyond window
          expect(store.liveTail.get('a1')).toBe(60n)
          expect(store.caughtUpToLiveTail('a1')).toBe(false)

          // The server is now authoritatively empty: LATEST returns no messages.
          mockListAgentMessages.mockResolvedValueOnce({ messages: [], hasMore: false })
          await store.jumpToLatestMessages('w1', 'a1')

          expect(store.getMessages('a1')).toHaveLength(0)
          expect(store.liveTail.get('a1')).toBe(0n) // vanished tail cleared
          expect(store.caughtUpToLiveTail('a1')).toBe(true) // wedge resolved
          dispose()
        })
      })

      it('does NOT clear the live tail when jump-to-latest still returns messages', async () => {
        await createRoot(async (dispose) => {
          const store = createChatStore()
          store.setMessages('a1', Array.from({ length: 50 }, (_, i) => makeMessage(`m${i}`, BigInt(i + 1))))
          store.trimNewestEnd('a1', 30) // hasMoreNewer=true, latestLiveSeq=50
          // The server still has the latest page: jump catches up normally.
          mockListAgentMessages.mockResolvedValueOnce({
            messages: Array.from({ length: 50 }, (_, i) => makeMessage(`m${i}`, BigInt(i + 1))),
            hasMore: false,
          })
          await store.jumpToLatestMessages('w1', 'a1')
          expect(store.getLastSeq('a1')).toBe(50n)
          expect(store.liveTail.get('a1')).toBe(50n) // not cleared
          expect(store.caughtUpToLiveTail('a1')).toBe(true)
          dispose()
        })
      })
    })

    describe('command-stream pruning on trim/remove', () => {
      it('spares a break-only (inactive but buffered) span dropped by an oldest-end trim, reclaiming it on a sweep', () => {
        createRoot((dispose) => {
          const store = createChatStore()
          store.setMessages('a1', Array.from({ length: 50 }, (_, i) => makeToolUseSpan(`m${i}`, BigInt(i + 1), `span${i}`)))
          // A content-less reasoning-summary break records a segment WITHOUT marking
          // the span renderable (only real text flips the renderable bit), so span0 has a
          // buffer but is NOT renderable. The recorded part boundary would still be LOST
          // if pruned, so the survivor guard spares a span by its BUFFER, not just its
          // renderable bit -- and records it as orphaned so the buffer stays bounded.
          store.appendCommandStream('a1', 'span0', 'item/reasoning/summaryPartAdded', '')
          store.appendCommandStream('a1', 'span49', 'item/reasoning/summaryPartAdded', '')
          expect(store.getCommandStream('a1', 'span0')).toHaveLength(1)
          expect(store.hasRenderableCommandStream('a1', 'span0')).toBe(false)

          store.trimOldestEnd('a1', 30) // keep newest 30 (seq 21..50); drop span0..span19
          expect(store.getMessages('a1').find(m => m.spanId === 'span0')).toBeUndefined()
          // span0's break-only buffer is SPARED (not pruned outright); span49 (still
          // in the window) is untouched.
          expect(store.getCommandStream('a1', 'span0')).toHaveLength(1)
          expect(store.getCommandStream('a1', 'span49')).toHaveLength(1)
          // Still bounded: the turn-end / catch-up sweep reclaims the now-unreferenced
          // orphan, just at sweep time rather than trim time.
          store.sweepOrphanedBufferedSpans('a1')
          expect(store.getCommandStream('a1', 'span0')).toHaveLength(0)
          expect(store.getCommandStream('a1', 'span49')).toHaveLength(1)
          dispose()
        })
      })

      it('spares an ACTIVE span dropped by an oldest-end trim, reclaiming it on a sweep', () => {
        createRoot((dispose) => {
          const store = createChatStore()
          store.setMessages('a1', Array.from({ length: 50 }, (_, i) => makeToolUseSpan(`m${i}`, BigInt(i + 1), `span${i}`)))
          store.appendCommandStream('a1', 'span0', 'item/commandExecution/output', 'old output') // marks span0 active
          store.appendCommandStream('a1', 'span49', 'item/commandExecution/output', 'new output')
          expect(store.hasRenderableCommandStream('a1', 'span0')).toBe(true)

          store.trimOldestEnd('a1', 30) // keep newest 30 (seq 21..50); drop span0..span19
          expect(store.getMessages('a1').find(m => m.spanId === 'span0')).toBeUndefined()
          // Unlike an inactive buffer (pruned outright), an ACTIVE oldest-end span is
          // SPARED and recorded as orphaned: a long-running tool whose whole exchange
          // is the oldest content while it still streams keeps its mid-flight segments
          // instead of re-vivifying from empty on the next delta.
          expect(store.getCommandStream('a1', 'span0')).toHaveLength(1)
          expect(store.getCommandStream('a1', 'span49')).toHaveLength(1)
          // The buffer is still bounded -- the turn-end / catch-up sweep reclaims the
          // now-unreferenced orphan, just at sweep time rather than trim time.
          store.sweepOrphanedBufferedSpans('a1')
          expect(store.getCommandStream('a1', 'span0')).toHaveLength(0)
          expect(store.getCommandStream('a1', 'span49')).toHaveLength(1)
          dispose()
        })
      })

      it('keeps a span stream when an oldest-end trim splits a pair sharing the spanId', () => {
        createRoot((dispose) => {
          const store = createChatStore()
          // 50 spans, each unique -- except a tool_use opener (seq 20) and its
          // tool_result (seq 21) that SHARE one spanId, straddling the oldest-trim
          // boundary (keep newest 30 = seq 21..50, drop seq 1..20).
          const msgs = Array.from({ length: 50 }, (_, i) => makeToolUseSpan(`m${i}`, BigInt(i + 1), `span${i}`))
          msgs[19] = makeToolUseSpan('op', 20n, 'shared') // dropped (oldest end)
          msgs[20] = makeToolResultSpan('res', 21n, 'shared') // kept (in window)
          store.setMessages('a1', msgs)
          store.appendCommandStream('a1', 'shared', 'item/commandExecution/output', 'output')
          expect(store.getCommandStream('a1', 'shared')).toHaveLength(1)

          store.trimOldestEnd('a1', 30)
          // The opener is dropped, but its result survives and still renders the
          // span's stream -- pruning 'shared' (whose dropped opener carried it)
          // would blank the kept result. The surviving-row guard must spare it.
          expect(store.getMessages('a1').find(m => m.id === 'op')).toBeUndefined()
          expect(store.getMessages('a1').find(m => m.id === 'res')).toBeDefined()
          expect(store.getCommandStream('a1', 'shared')).toHaveLength(1)
          dispose()
        })
      })

      it('spares a still-buffered command stream when a reseq moves its row beyond the window', () => {
        createRoot((dispose) => {
          const store = createChatStore()
          store.setMessages('a1', Array.from({ length: 50 }, (_, i) => makeToolUseSpan(`m${i}`, BigInt(i + 1), `span${i}`)))
          store.trimNewestEnd('a1', 30) // window seq 1..30, hasMoreNewer=true, latestLiveSeq=50
          // span4's row (m4, seq 5) is mid-flight: appending marks the span renderable.
          store.appendCommandStream('a1', 'span4', 'item/commandExecution/output', 'live output')
          expect(store.getCommandStream('a1', 'span4')).toHaveLength(1)

          // m4 is consolidated on the backend and reseq'd to a brand-new tail seq
          // beyond the loaded window, so it's dropped from its old position.
          store.addMessage('a1', makeToolUseSpan('m4', 60n, 'span4', 5n))
          expect(store.getMessages('a1').find(m => m.id === 'm4')).toBeUndefined()
          // Unlike an explicit removeMessage (which clears regardless), the reseq
          // drop SPARES a still-buffered stream: the span is mid-flight at the live
          // tail, so its buffer must survive or the next delta re-vivifies from
          // empty. No surviving row references span4, so only the active guard saves it.
          expect(store.getCommandStream('a1', 'span4')).toHaveLength(1)
          // ...and the spared stream is RECORDED as orphaned, just like removeMessage,
          // so a turn-end sweep reclaims it if it never ends -- without the record it
          // would leak for the session.
          store.sweepOrphanedBufferedSpans('a1')
          expect(store.getCommandStream('a1', 'span4')).toHaveLength(0)
          dispose()
        })
      })

      it('spares + records a mid-stream span dropped by a full-window replace, reclaiming it on a sweep', () => {
        createRoot((dispose) => {
          const store = createChatStore()
          store.setMessages('a1', Array.from({ length: 50 }, (_, i) => makeToolUseSpan(`m${i}`, BigInt(i + 1), `span${i}`)))
          // span4 is mid-flight (buffered + renderable).
          store.appendCommandStream('a1', 'span4', 'item/commandExecution/output', 'live output')
          expect(store.getCommandStream('a1', 'span4')).toHaveLength(1)

          // A full-window replace (jump-to-oldest / reconnect snapshot landing on a
          // non-tail page) drops span4. Like the trims/delete, the replace must spare
          // its mid-flight buffer AND record it as orphaned -- without the record the
          // buffer would neither clear nor be swept, leaking for the session.
          store.setMessages('a1', [makeMessage('x1', 100n), makeMessage('x2', 101n)])
          expect(store.getMessages('a1').find(m => m.spanId === 'span4')).toBeUndefined()
          expect(store.getCommandStream('a1', 'span4')).toHaveLength(1) // spared
          store.sweepOrphanedBufferedSpans('a1')
          expect(store.getCommandStream('a1', 'span4')).toHaveLength(0) // reclaimed
          dispose()
        })
      })

      it('clears a non-referenced span\'s ended (unbuffered) stream on a full-window replace', () => {
        createRoot((dispose) => {
          const store = createChatStore()
          store.setMessages('a1', [makeToolUseSpan('m1', 1n, 'span1')])
          store.appendCommandStream('a1', 'span1', 'item/commandExecution/output', 'output')
          // The stream ENDS: clearCommandStream drops the buffer, so span1 is no
          // longer buffered. A subsequent full-window replace that drops m1 has
          // nothing to spare -- the prune is a clean no-op, not a leak.
          store.clearCommandStream('a1', 'span1')
          expect(store.getCommandStream('a1', 'span1')).toHaveLength(0)
          store.setMessages('a1', [makeMessage('x1', 100n)])
          expect(store.getMessages('a1').find(m => m.spanId === 'span1')).toBeUndefined()
          expect(store.getCommandStream('a1', 'span1')).toHaveLength(0)
          dispose()
        })
      })

      it('spares an explicitly removed span\'s BUFFERED stream, then reclaims it on a turn-end sweep', () => {
        createRoot((dispose) => {
          const store = createChatStore()
          store.addMessage('a1', makeToolUseSpan('m1', 1n, 'span1'))
          store.appendCommandStream('a1', 'span1', 'item/commandExecution/output', 'output')
          expect(store.getCommandStream('a1', 'span1')).toHaveLength(1)
          // The row is deleted while its tool is still mid-flight: clearing now
          // would lose the in-progress segments, so the buffer is SPARED and
          // recorded as orphaned.
          store.removeMessage('a1', 'm1')
          expect(store.getCommandStream('a1', 'span1')).toHaveLength(1)
          // The turn-end sweep reclaims the now-unreferenced orphan (this stream
          // never produced a stream-end).
          store.sweepOrphanedBufferedSpans('a1')
          expect(store.getCommandStream('a1', 'span1')).toHaveLength(0)
          dispose()
        })
      })

      it('records a break-only span dropped by a newest-end trim, reclaiming it on a sweep', () => {
        createRoot((dispose) => {
          const store = createChatStore()
          store.setMessages('a1', Array.from({ length: 50 }, (_, i) => makeToolUseSpan(`m${i}`, BigInt(i + 1), `span${i}`)))
          // span49 (a dropped newest row) buffers only a content-less break:
          // inactive, but holding a recorded part boundary a prune would lose.
          store.appendCommandStream('a1', 'span49', 'item/reasoning/summaryPartAdded', '')
          expect(store.hasRenderableCommandStream('a1', 'span49')).toBe(false)

          store.trimNewestEnd('a1', 30) // keep oldest 30 (seq 1..30); drop span30..span49
          expect(store.getMessages('a1').find(m => m.spanId === 'span49')).toBeUndefined()
          // The dropped newest span's break-only buffer is SPARED -- a later text
          // delta continues from the recorded break instead of re-vivifying empty...
          expect(store.getCommandStream('a1', 'span49')).toHaveLength(1)
          // ...AND recorded as orphaned, exactly like trimOldestEnd / removeMessage.
          // The spare normally re-fetches and ends on scroll-back, but a reasoning
          // item abandoned before completion (no stream-end ever clears it) would
          // otherwise leak for the session; recording it lets the turn-end /
          // catch-up sweep reclaim it once no surviving row references it.
          store.sweepOrphanedBufferedSpans('a1')
          expect(store.getCommandStream('a1', 'span49')).toHaveLength(0)
          dispose()
        })
      })

      it('keeps a still-referenced orphan recorded across a sweep, reclaiming it once its row is gone', () => {
        createRoot((dispose) => {
          const store = createChatStore()
          store.addMessage('a1', makeToolUseSpan('m1', 1n, 'span1'))
          store.appendCommandStream('a1', 'span1', 'item/commandExecution/output', 'output')
          // Delete the row mid-flight: the active buffer is spared and recorded.
          store.removeMessage('a1', 'm1')
          expect(store.getCommandStream('a1', 'span1')).toHaveLength(1)
          // The row returns (a scroll-back re-fetch), so span1 is recorded AND
          // referenced again. A sweep now must SKIP it (still rendered) -- but must
          // NOT forget the record: otherwise a later drop of that row via a
          // non-recording path (jump-to-latest's full-window replace) would leave
          // the buffer both unreferenced and untracked, leaking for the session.
          store.setMessages('a1', [makeToolUseSpan('m1', 1n, 'span1')])
          store.sweepOrphanedBufferedSpans('a1')
          expect(store.getCommandStream('a1', 'span1')).toHaveLength(1) // spared, still referenced

          // The row leaves again without re-recording (full-window replace).
          store.setMessages('a1', [])
          // The orphan is still recorded from the first record, so this sweep
          // reclaims it instead of leaking.
          store.sweepOrphanedBufferedSpans('a1')
          expect(store.getCommandStream('a1', 'span1')).toHaveLength(0)
          dispose()
        })
      })

      it('spares a removed span\'s break-only buffer, reclaiming it on a turn-end sweep', () => {
        createRoot((dispose) => {
          const store = createChatStore()
          store.addMessage('a1', makeToolUseSpan('m1', 1n, 'span1'))
          // span1 buffers only a content-less break (inactive). The old isActive guard
          // would clear it on delete, losing the recorded boundary; the buffer-based
          // guard spares and records it instead.
          store.appendCommandStream('a1', 'span1', 'item/reasoning/summaryPartAdded', '')
          expect(store.hasRenderableCommandStream('a1', 'span1')).toBe(false)
          store.removeMessage('a1', 'm1')
          expect(store.getCommandStream('a1', 'span1')).toHaveLength(1)
          store.sweepOrphanedBufferedSpans('a1')
          expect(store.getCommandStream('a1', 'span1')).toHaveLength(0)
          dispose()
        })
      })

      it('reclaims a spared orphan on its normal stream-end (no sweep needed)', () => {
        createRoot((dispose) => {
          const store = createChatStore()
          store.addMessage('a1', makeToolUseSpan('m1', 1n, 'span1'))
          store.appendCommandStream('a1', 'span1', 'item/commandExecution/output', 'output')
          store.removeMessage('a1', 'm1') // spared (active), orphaned
          expect(store.getCommandStream('a1', 'span1')).toHaveLength(1)
          // The stream completes normally: clearCommandStream drops the buffer AND
          // forgets the orphan, so a later sweep is a no-op.
          store.clearCommandStream('a1', 'span1')
          expect(store.getCommandStream('a1', 'span1')).toHaveLength(0)
          store.appendCommandStream('a1', 'span1', 'item/commandExecution/output', 're-vivified')
          store.sweepOrphanedBufferedSpans('a1') // must NOT clear the new buffer
          expect(store.getCommandStream('a1', 'span1')).toHaveLength(1)
          dispose()
        })
      })

      it('keeps a span command stream while a sibling row still references the spanId', () => {
        createRoot((dispose) => {
          const store = createChatStore()
          // A tool_use opener and its tool_result share one spanId.
          store.addMessage('a1', makeToolUseSpan('op', 1n, 'span1'))
          store.addMessage('a1', makeToolUseSpan('res', 2n, 'span1'))
          store.appendCommandStream('a1', 'span1', 'item/commandExecution/output', 'output')
          expect(store.getCommandStream('a1', 'span1')).toHaveLength(1)

          // Deleting only one member must NOT wipe the stream the survivor renders.
          store.removeMessage('a1', 'op')
          expect(store.getCommandStream('a1', 'span1')).toHaveLength(1)
          // The sweep must NOT clear it either while a row still references the span.
          store.sweepOrphanedBufferedSpans('a1')
          expect(store.getCommandStream('a1', 'span1')).toHaveLength(1)

          // Deleting the LAST member spares the still-buffered stream (it could be
          // mid-flight); the turn-end sweep reclaims the now-unreferenced orphan.
          store.removeMessage('a1', 'res')
          expect(store.getCommandStream('a1', 'span1')).toHaveLength(1)
          store.sweepOrphanedBufferedSpans('a1')
          expect(store.getCommandStream('a1', 'span1')).toHaveLength(0)
          dispose()
        })
      })

      it('does NOT prune a span streaming ahead of its (not-yet-loaded) message', () => {
        createRoot((dispose) => {
          const store = createChatStore()
          store.setMessages('a1', Array.from({ length: 50 }, (_, i) => makeToolUseSpan(`m${i}`, BigInt(i + 1), `span${i}`)))
          // A stream arrives for a span whose tool_use message hasn't loaded yet.
          store.appendCommandStream('a1', 'pending-span', 'item/commandExecution/output', 'live')
          store.trimOldestEnd('a1', 30) // drops only loaded oldest spans, never the orphan
          expect(store.getCommandStream('a1', 'pending-span')).toHaveLength(1)
          dispose()
        })
      })

      it('spares an actively-streaming span from a newest-end trim (scroll-up while the agent works)', () => {
        createRoot((dispose) => {
          const store = createChatStore()
          store.setMessages('a1', Array.from({ length: 50 }, (_, i) => makeToolUseSpan(`m${i}`, BigInt(i + 1), `span${i}`)))
          // span49 is the live tail span, mid-stream; span40 streamed then completed.
          store.appendCommandStream('a1', 'span49', 'item/commandExecution/output', 'live tail')
          store.appendCommandStream('a1', 'span40', 'item/commandExecution/output', 'done')
          store.clearCommandStream('a1', 'span40') // completion clears buffer + renderable bit
          expect(store.getCommandStream('a1', 'span49')).toHaveLength(1)

          store.trimNewestEnd('a1', 30) // keep oldest 30 (seq 1..30); drop span30..span49
          expect(store.getMessages('a1').find(m => m.spanId === 'span49')).toBeUndefined()
          // The still-streaming tail span's buffer SURVIVES the newest-end trim --
          // clearing it would drop the in-progress segments and re-vivify from empty.
          // A completed span (already cleared) has nothing left to leak.
          expect(store.getCommandStream('a1', 'span49')).toHaveLength(1)
          expect(store.getCommandStream('a1', 'span40')).toHaveLength(0)
          dispose()
        })
      })

      it('spares an actively-streaming span when a reseq drops the row beyond the window', () => {
        createRoot((dispose) => {
          const store = createChatStore()
          store.setMessages('a1', Array.from({ length: 50 }, (_, i) => makeToolUseSpan(`m${i}`, BigInt(i + 1), `span${i}`)))
          store.trimNewestEnd('a1', 30) // window seq 1..30, hasMoreNewer=true
          // m5 (seq 6, span5) is inside the window and mid-stream.
          store.appendCommandStream('a1', 'span5', 'item/commandExecution/output', 'live')
          expect(store.getCommandStream('a1', 'span5')).toHaveLength(1)

          // m5 is reseq'd (notification consolidation) to a tail seq beyond the
          // scrolled-away window, so the row is dropped from its old position...
          store.addMessage('a1', makeToolUseSpan('m5', 60n, 'span5', 6n))
          expect(store.getMessages('a1').find(m => m.id === 'm5')).toBeUndefined()
          // ...but its live stream is SPARED -- it's still streaming at the tail.
          expect(store.getCommandStream('a1', 'span5')).toHaveLength(1)
          dispose()
        })
      })
    })

    describe('jumpToLatestMessages', () => {
      it('snaps to the live tail and is gap-free across a mid-fetch live message', async () => {
        await createRoot(async (dispose) => {
          mockListAgentMessages.mockClear()
          const store = createChatStore()
          store.setMessages('a1', Array.from({ length: 50 }, (_, i) => makeMessage(`m${i}`, BigInt(i + 1))))
          store.trimNewestEnd('a1', 30) // window seq 1..30, hasMoreNewer=true
          // Two live messages arrive while scrolled away; both dropped but recorded.
          store.addMessage('a1', makeMessage('live55', 55n))
          store.addMessage('a1', makeMessage('live60', 60n))
          expect(store.liveTail.get('a1')).toBe(60n)

          // jumpToLatest: the latest page only reaches seq 55 (raced the broadcast),
          // so a forward fill is needed to cover seq 56..60.
          mockListAgentMessages.mockResolvedValueOnce({
            messages: Array.from({ length: 40 }, (_, i) => makeMessage(`p${i}`, BigInt(i + 16))), // seq 16..55
            hasMore: true,
          })
          mockListAgentMessages.mockResolvedValueOnce({
            messages: Array.from({ length: 5 }, (_, i) => makeMessage(`q${i}`, BigInt(i + 56))), // seq 56..60
            hasMore: false,
          })

          await store.jumpToLatestMessages('w1', 'a1')
          const msgs = store.getMessages('a1')
          expect(msgs.at(-1)!.seq).toBe(60n) // reached the true tail
          // Contiguous, no gap and no duplicate.
          const seqs = msgs.map(m => m.seq)
          for (let i = 1; i < seqs.length; i++)
            expect(seqs[i]).toBe(seqs[i - 1] + 1n)
          expect(store.hasNewerMessages('a1')).toBe(false)
          dispose()
        })
      })

      it('keeps the followed live tail lean at the BASE cap during a large forward fill', async () => {
        await createRoot(async (dispose) => {
          mockListAgentMessages.mockClear()
          const store = createChatStore()
          const pageOf = (start: number, count: number) =>
            Array.from({ length: count }, (_, i) => makeMessage(`m${start + i}`, BigInt(start + i)))
          // Scrolled far away: window seq 1..30, hasMoreNewer=true.
          store.setMessages('a1', pageOf(1, 50))
          store.trimNewestEnd('a1', 30)
          // A burst raised the recorded live tail to seq 200 while we were away (each
          // live message dropped from the window but recorded as latestLiveSeq).
          store.addMessage('a1', makeMessage('live200', 200n))
          expect(store.liveTail.get('a1')).toBe(200n)

          // jumpToLatest re-anchors on a LATEST page that raced the burst (reaches only
          // seq 50), so the gap-free forward fill pages AFTER to the live tail -- 150+
          // rows across four pages.
          mockListAgentMessages.mockResolvedValueOnce({ messages: pageOf(1, 50), hasMore: false }) // LATEST -> 1..50
          mockListAgentMessages.mockResolvedValueOnce({ messages: pageOf(51, 50), hasMore: true }) // AFTER 50 -> 51..100
          mockListAgentMessages.mockResolvedValueOnce({ messages: pageOf(101, 50), hasMore: true }) // AFTER 100 -> 101..150
          mockListAgentMessages.mockResolvedValueOnce({ messages: pageOf(151, 50), hasMore: false }) // AFTER 150 -> 151..200 (tail)

          await store.jumpToLatestMessages('w1', 'a1')

          const msgs = store.getMessages('a1')
          // forwardFillToLiveTail trims each page to the BASE cap (150), NOT the
          // ceiling: a chat being followed at the live tail stays lean. A regression
          // capping this trim at MAX_LOADED_CHAT_MESSAGES_CEILING would keep all 200.
          expect(msgs.length).toBe(MAX_LOADED_CHAT_MESSAGES)
          expect(msgs.at(-1)!.seq).toBe(200n) // reached the live tail
          expect(msgs[0].seq).toBe(51n) // oldest end trimmed to keep the newest 150
          expect(store.hasOlderMessages('a1')).toBe(true) // trimming exposed older history
          expect(store.hasNewerMessages('a1')).toBe(false) // at the tail
          dispose()
        })
      })

      it('terminates with one auto-retry when a forward page advances neither the tail nor reports has_more=false', async () => {
        await createRoot(async (dispose) => {
          mockListAgentMessages.mockClear()
          const store = createChatStore()
          store.setMessages('a1', Array.from({ length: 50 }, (_, i) => makeMessage(`m${i}`, BigInt(i + 1)))) // seq 1..50
          store.trimNewestEnd('a1', 30) // window seq 1..30, hasMoreNewer=true
          // A live message bumps latestLiveSeq beyond anything the server still
          // holds (e.g. it was rolled back / deleted server-side).
          store.addMessage('a1', makeMessage('live100', 100n)) // dropped, latestLiveSeq=100

          // LATEST page reaches only seq 50; the gap-free loop then forward-fetches.
          mockListAgentMessages.mockResolvedValueOnce({
            messages: Array.from({ length: 50 }, (_, i) => makeMessage(`m${i}`, BigInt(i + 1))), // seq 1..50
            hasMore: false,
          })
          // Every subsequent AFTER fetch returns a non-empty page that is ENTIRELY
          // already in the window (so the merge advances getLastSeq by nothing) yet
          // claims has_more=true -- the spin-forever trap the cursor guard breaks.
          mockListAgentMessages.mockResolvedValue({
            messages: Array.from({ length: 50 }, (_, i) => makeMessage(`m${i}`, BigInt(i + 1))), // seq 1..50 dups
            hasMore: true,
          })

          await store.jumpToLatestMessages('w1', 'a1')
          // It returned (no infinite loop): the LATEST page, the non-advancing AFTER
          // fetch that tripped the stall guard, then ONE direct auto-retry anchored
          // just below latestLiveSeq (cursor 99) before it gives up and sticks.
          expect(mockListAgentMessages).toHaveBeenCalledTimes(3)
          expect(mockListAgentMessages).toHaveBeenLastCalledWith('w1', {
            agentId: 'a1',
            anchor: MessagePageAnchor.AFTER,
            cursorSeq: 99n, // latestLiveSeq (100) - 1
            limit: 50,
          })
          expect(store.getLastSeq('a1')).toBe(50n) // still unreachable, so we stuck anyway
          // The unreachable seq 100 is clamped out of latestLiveSeq so a later
          // loadNewerPage/scroll doesn't re-wedge hasMoreNewer chasing the gap.
          expect(store.liveTail.get('a1')).toBe(50n)
          expect(store.hasNewerMessages('a1')).toBe(false)
          dispose()
        })
      })

      it('auto-retry from the tail cursor recovers the live tail after a dedup-stall', async () => {
        await createRoot(async (dispose) => {
          mockListAgentMessages.mockClear()
          const store = createChatStore()
          store.setMessages('a1', Array.from({ length: 50 }, (_, i) => makeMessage(`m${i}`, BigInt(i + 1)))) // seq 1..50
          store.trimNewestEnd('a1', 30) // window seq 1..30, hasMoreNewer=true
          store.addMessage('a1', makeMessage('live52', 52n)) // dropped, latestLiveSeq=52

          // LATEST reaches seq 50.
          mockListAgentMessages.mockResolvedValueOnce({
            messages: Array.from({ length: 50 }, (_, i) => makeMessage(`m${i}`, BigInt(i + 1))),
            hasMore: false,
          })
          // The plain AFTER(50) page stalls (returns dups, has_more=true)...
          mockListAgentMessages.mockResolvedValueOnce({
            messages: Array.from({ length: 50 }, (_, i) => makeMessage(`m${i}`, BigInt(i + 1))),
            hasMore: true,
          })
          // ...but the auto-retry anchored at latestLiveSeq-1 (cursor 51) reaches the
          // genuine tail (seq 52), healing the gap.
          mockListAgentMessages.mockResolvedValueOnce({
            messages: [makeMessage('m51', 51n), makeMessage('live52', 52n)],
            hasMore: false,
          })

          await store.jumpToLatestMessages('w1', 'a1')
          expect(mockListAgentMessages).toHaveBeenCalledTimes(3)
          expect(mockListAgentMessages).toHaveBeenLastCalledWith('w1', {
            agentId: 'a1',
            anchor: MessagePageAnchor.AFTER,
            cursorSeq: 51n, // latestLiveSeq (52) - 1
            limit: 50,
          })
          expect(store.getLastSeq('a1')).toBe(52n) // tail recovered
          expect(store.hasNewerMessages('a1')).toBe(false)
          dispose()
        })
      })

      it('preserves a message broadcast mid-jump instead of clamping it out of latestLiveSeq', async () => {
        await createRoot(async (dispose) => {
          mockListAgentMessages.mockClear()
          const store = createChatStore()
          store.setMessages('a1', Array.from({ length: 50 }, (_, i) => makeMessage(`m${i}`, BigInt(i + 1)))) // seq 1..50
          store.trimNewestEnd('a1', 30) // window 1..30, hasMoreNewer=true, latestLiveSeq=50
          // The LATEST fetch races a live broadcast: while it is in flight (and
          // hasMoreNewer is still true), seq 70 is broadcast, so the live-append
          // guard drops it but records it as the live tail (latestLiveSeq=70).
          mockListAgentMessages.mockImplementationOnce(async () => {
            store.addMessage('a1', makeMessage('live70', 70n))
            return { messages: Array.from({ length: 50 }, (_, i) => makeMessage(`m${i}`, BigInt(i + 1))), hasMore: false } // 1..50
          })
          // Every subsequent AFTER fetch can't yet see seq 70 (committed after the
          // query snapshot): it returns only already-present rows -- a dedup-stall.
          mockListAgentMessages.mockResolvedValue({
            messages: Array.from({ length: 50 }, (_, i) => makeMessage(`m${i}`, BigInt(i + 1))), // 1..50 dups
            hasMore: true,
          })

          await store.jumpToLatestMessages('w1', 'a1')
          // seq 70 stayed unreachable across fill+retry, so the window sticks at 50.
          expect(store.getLastSeq('a1')).toBe(50n)
          // ...but latestLiveSeq is NOT clamped down to 50: seq 70 advanced past the
          // entry snapshot (it arrived during the jump), so it is preserved for
          // later recovery rather than silently forgotten.
          expect(store.liveTail.get('a1')).toBe(70n)
          dispose()
        })
      })

      it('re-attempts the forward fill across rounds to pull a tail that advances in steps', async () => {
        await createRoot(async (dispose) => {
          mockListAgentMessages.mockClear()
          const store = createChatStore()
          store.setMessages('a1', Array.from({ length: 50 }, (_, i) => makeMessage(`m${i}`, BigInt(i + 1)))) // seq 1..50
          store.trimNewestEnd('a1', 30) // window 1..30, hasMoreNewer=true, latestLiveSeq=50
          store.addMessage('a1', makeMessage('live70', 70n)) // dropped, latestLiveSeq=70

          // call 1 LATEST: reaches seq 50.
          mockListAgentMessages.mockResolvedValueOnce({
            messages: Array.from({ length: 50 }, (_, i) => makeMessage(`m${i}`, BigInt(i + 1))),
            hasMore: false,
          })
          // call 2 AFTER(50): advances the tail partway (51..60) but not to 70.
          mockListAgentMessages.mockResolvedValueOnce({
            messages: Array.from({ length: 10 }, (_, i) => makeMessage(`p${i}`, BigInt(i + 51))), // 51..60
            hasMore: true,
          })
          // call 3 AFTER(60): a dedup-stall (same rows, no advance) ends round 0's fill.
          mockListAgentMessages.mockResolvedValueOnce({
            messages: Array.from({ length: 10 }, (_, i) => makeMessage(`p${i}`, BigInt(i + 51))), // 51..60 dups
            hasMore: true,
          })
          // call 4 retry AFTER(69): still can't see the tail (dups) -- round 0 made
          // progress (50 -> 60), so the OUTER loop re-attempts.
          mockListAgentMessages.mockResolvedValueOnce({
            messages: Array.from({ length: 10 }, (_, i) => makeMessage(`p${i}`, BigInt(i + 51))), // 51..60 dups
            hasMore: true,
          })
          // call 5 round 1 AFTER(60): the tail has now caught up; pull 61..70.
          mockListAgentMessages.mockResolvedValueOnce({
            messages: Array.from({ length: 10 }, (_, i) => makeMessage(`q${i}`, BigInt(i + 61))), // 61..70
            hasMore: false,
          })

          await store.jumpToLatestMessages('w1', 'a1')
          expect(store.getLastSeq('a1')).toBe(70n) // tail reached via the second round
          expect(store.hasNewerMessages('a1')).toBe(false)
          dispose()
        })
      })
    })

    describe('jumpToOldestMessages', () => {
      it('snaps to the earliest page: hasMoreOlder false, hasMoreNewer reflects has_more', async () => {
        await createRoot(async (dispose) => {
          mockListAgentMessages.mockClear()
          const store = createChatStore()
          // Window sits at the live tail (seq 151..200); older history exists.
          store.setMessages('a1', Array.from({ length: 50 }, (_, i) => makeMessage(`m${i}`, BigInt(i + 151))), false)
          mockListAgentMessages.mockResolvedValueOnce({
            messages: Array.from({ length: 50 }, (_, i) => makeMessage(`o${i}`, BigInt(i + 1))), // seq 1..50
            hasMore: true,
          })

          await store.jumpToOldestMessages('w1', 'a1')
          const msgs = store.getMessages('a1')
          expect(msgs[0].seq).toBe(1n)
          expect(msgs.at(-1)!.seq).toBe(50n)
          expect(store.hasOlderMessages('a1')).toBe(false) // at the very start
          expect(store.hasNewerMessages('a1')).toBe(true) // more exist beyond it
          expect(mockListAgentMessages).toHaveBeenCalledWith('w1', { agentId: 'a1', anchor: MessagePageAnchor.OLDEST, limit: 50 })
          dispose()
        })
      })
    })

    describe('applyMessages preserves optimistic local messages', () => {
      it('keeps an unsent local (seq 0n) with no echo across a window replacement', () => {
        createRoot((dispose) => {
          const store = createChatStore()
          store.setMessages('a1', [makeMessage('m1', 1n)])
          store.addMessage('a1', makeUserMessage('local-1', 0n, 'unsent draft'))
          // A reconnect snapshot replaces the window; the unsent local has no echo.
          store.setMessages('a1', [makeMessage('m1', 1n), makeMessage('m2', 2n)])
          const msgs = store.getMessages('a1')
          expect(msgs.at(-1)!.id).toBe('local-1')
          expect(msgs.filter(m => m.seq === 0n)).toHaveLength(1)
          dispose()
        })
      })

      it('drops a local once its server echo is in the replacement page', () => {
        createRoot((dispose) => {
          const store = createChatStore()
          store.setMessages('a1', [makeMessage('m1', 1n)])
          store.addMessage('a1', makeUserMessage('local-1', 0n, 'hello world'))
          // The snapshot now carries the echoed message (same user-content signature).
          store.setMessages('a1', [makeMessage('m1', 1n), makeUserMessage('server-2', 2n, 'hello world')])
          const msgs = store.getMessages('a1')
          expect(msgs.filter(m => m.seq === 0n)).toHaveLength(0) // reconciled away
          expect(msgs.at(-1)!.id).toBe('server-2')
          dispose()
        })
      })

      it('reconciles a failed local across a window replace when the page echoes its text (delivery is truth)', () => {
        createRoot((dispose) => {
          const store = createChatStore()
          store.setMessages('a1', [makeMessage('m1', 1n)])
          // A send marked failed whose text the replacement page echoes: it WAS delivered,
          // so under "delivery is truth" the failed bubble reconciles to the echo.
          store.addMessage('a1', makeUserMessage('local-failed', 0n, 'hello', 'Failed to deliver'))
          store.setMessages('a1', [makeMessage('m1', 1n), makeUserMessage('server-2', 2n, 'hello')])
          const msgs = store.getMessages('a1')
          expect(msgs.some(m => m.id === 'local-failed')).toBe(false) // reconciled to the echo
          expect(msgs.some(m => m.id === 'server-2')).toBe(true)
          expect(store.messageErrors()['local-failed']).toBeUndefined() // annotation reclaimed
          dispose()
        })
      })

      it('an echo prefers a pending local over a same-text failed one (does not strand the pending send)', () => {
        createRoot((dispose) => {
          const store = createChatStore()
          store.setMessages('a1', [makeMessage('m1', 1n)])
          // A previously-FAILED "hi" (hydrated, proto deliveryError) and a later PENDING
          // "hi" retry share the tail. A SINGLE echo arrives: it must reconcile the
          // PENDING retry (the send awaiting an echo), NOT steal the echo for the failed
          // one and strand the pending send forever. The failed bubble survives (its own
          // echo never came).
          store.addMessage('a1', makeUserMessage('local-failed', 0n, 'hi', 'Failed to deliver'))
          store.addMessage('a1', makeUserMessage('local-pending', 0n, 'hi'))
          store.setMessages('a1', [makeMessage('m1', 1n), makeUserMessage('server-hi', 2n, 'hi')])
          const msgs = store.getMessages('a1')
          expect(msgs.some(m => m.id === 'local-pending')).toBe(false) // reconciled to the echo
          expect(msgs.some(m => m.id === 'local-failed')).toBe(true) // failed bubble preserved
          expect(msgs.filter(m => m.seq === 0n).map(m => m.id)).toEqual(['local-failed'])
          dispose()
        })
      })

      it('reconciles only ONE of two identical-text locals when the page carries a single echo', () => {
        createRoot((dispose) => {
          const store = createChatStore()
          store.setMessages('a1', [makeMessage('m1', 1n)])
          // The user fires the same text twice before either echo arrives, so two
          // optimistic locals share one signature.
          store.addMessage('a1', makeUserMessage('local-ok-1', 0n, 'ok'))
          store.addMessage('a1', makeUserMessage('local-ok-2', 0n, 'ok'))
          // The replacement page carries only ONE server echo (the second send is
          // still in flight / fell beyond the page). Each echo reconciles at most
          // one local, so exactly one local survives -- not zero (which would drop
          // the still-pending duplicate send) nor two.
          store.setMessages('a1', [makeMessage('m1', 1n), makeUserMessage('server-ok', 2n, 'ok')])
          const locals = store.getMessages('a1').filter(m => m.seq === 0n)
          expect(locals).toHaveLength(1)
          expect(locals[0].id).toBe('local-ok-2') // earlier local consumed the echo
          expect(store.getMessages('a1').some(m => m.id === 'server-ok')).toBe(true)
          dispose()
        })
      })

      it('reconciles both identical-text locals when the page carries both echoes', () => {
        createRoot((dispose) => {
          const store = createChatStore()
          store.setMessages('a1', [makeMessage('m1', 1n)])
          store.addMessage('a1', makeUserMessage('local-ok-1', 0n, 'ok'))
          store.addMessage('a1', makeUserMessage('local-ok-2', 0n, 'ok'))
          // Both echoes present: both locals reconcile, both server bubbles render.
          store.setMessages('a1', [
            makeMessage('m1', 1n),
            makeUserMessage('server-ok-a', 2n, 'ok'),
            makeUserMessage('server-ok-b', 3n, 'ok'),
          ])
          expect(store.getMessages('a1').filter(m => m.seq === 0n)).toHaveLength(0)
          dispose()
        })
      })
    })

    describe('fetch supersede', () => {
      it('a jump supersedes an in-flight fetch so it cannot wedge pagination', async () => {
        await createRoot(async (dispose) => {
          mockListAgentMessages.mockClear()
          const store = createChatStore()
          store.setMessages('a1', Array.from({ length: 50 }, (_, i) => makeMessage(`m${i}`, BigInt(i + 1))))
          store.trimNewestEnd('a1', 30) // hasMoreNewer=true, window seq 1..30

          // A scroll-down fetch that never resolves would normally wedge
          // fetchingNewer (and thus all newer pagination) forever.
          let resolveStuck: (v: unknown) => void = () => {}
          mockListAgentMessages.mockReturnValueOnce(new Promise((r) => {
            resolveStuck = r
          }))
          const stuck = store.loadNewerPage('w1', 'a1')
          expect(store.isFetchingNewer('a1')).toBe(true)

          // A jump supersedes the stuck fetch and completes normally.
          mockListAgentMessages.mockResolvedValueOnce({
            messages: Array.from({ length: 50 }, (_, i) => makeMessage(`m${i}`, BigInt(i + 1))),
            hasMore: false,
          })
          await store.jumpToLatestMessages('w1', 'a1')
          expect(store.isFetchingNewer('a1')).toBe(false) // not wedged
          expect(store.hasNewerMessages('a1')).toBe(false) // landed at the tail

          // When the stuck fetch finally resolves, its result is discarded
          // (superseded) and it must not clobber the flag or inject a ghost row.
          resolveStuck({ messages: [makeMessage('ghost', 999n)], hasMore: true })
          await stuck
          expect(store.getMessages('a1').some(m => m.id === 'ghost')).toBe(false)
          expect(store.isFetchingNewer('a1')).toBe(false)
          dispose()
        })
      })

      it('a reconcile-driven jump aborted by the watch signal (no supersede) does not wedge fetchingNewer', async () => {
        await createRoot(async (dispose) => {
          mockListAgentMessages.mockReset()
          const store = createChatStore()
          store.setMessages('a1', Array.from({ length: 50 }, (_, i) => makeMessage(`m${i}`, BigInt(i + 1))))
          store.trimNewestEnd('a1', 30) // hasMoreNewer=true, window seq 1..30

          // The reconcile-driven empty-window re-seat / catch-up jump ties its fetch to
          // the WatchEvents subscription. Its LATEST fetch hangs, then the subscription
          // tears down (workspace switch) mid-flight -- aborting the fetch with NO
          // superseding beginHistoryFetch to reset the flags.
          let resolveHung: (v: unknown) => void = () => {}
          mockListAgentMessages.mockReturnValueOnce(new Promise((r) => {
            resolveHung = r
          }))
          const watch = new AbortController()
          const jump = store.jumpToLatestMessages('w1', 'a1', watch.signal)
          expect(store.isFetchingNewer('a1')).toBe(true)

          // Teardown: the watch signal aborts the in-flight fetch.
          watch.abort()
          // The hung RPC finally resolves; the body observes signal.aborted and bails.
          resolveHung({ messages: [], hasMore: false })
          await jump

          // The flag MUST be cleared: a watch-aborted fetch that wasn't superseded
          // otherwise stranded fetchingNewer=true, wedging loadNewerPage and the
          // empty-window re-seat (both gated on the flag) until an unrelated user fetch.
          expect(store.isFetchingNewer('a1')).toBe(false)

          // Proof the wedge is gone: a subsequent loadNewerPage actually fetches.
          mockListAgentMessages.mockResolvedValueOnce({ messages: [makeMessage('newpage', 31n)], hasMore: false })
          await store.loadNewerPage('w1', 'a1')
          expect(store.getMessages('a1').some(m => m.id === 'newpage')).toBe(true)
          dispose()
        })
      })

      it('a jump supersedes an in-flight INITIAL load so its stale page cannot clobber the tail', async () => {
        await createRoot(async (dispose) => {
          mockListAgentMessages.mockReset()
          const store = createChatStore()

          // The initial LATEST fetch hangs (e.g. slow cold start).
          let releaseInitial: (v: unknown) => void = () => {}
          mockListAgentMessages.mockReturnValueOnce(new Promise((r) => {
            releaseInitial = r
          }))
          const initial = store.loadInitialMessages('w1', 'a1')
          expect(store.isFetchingOlder('a1')).toBe(true)

          // A jump-to-latest fires while it's still in flight and lands fresh.
          mockListAgentMessages.mockResolvedValueOnce({ messages: [makeMessage('fresh', 99n)], hasMore: false })
          await store.jumpToLatestMessages('w1', 'a1')
          expect(store.getMessages('a1').map(m => m.id)).toEqual(['fresh'])

          // The stale initial page resolves last; superseded, it must be discarded
          // rather than overwriting the jump's window.
          releaseInitial({ messages: [makeMessage('stale', 1n)], hasMore: true })
          await initial
          expect(store.getMessages('a1').map(m => m.id)).toEqual(['fresh'])
          expect(store.hasNewerMessages('a1')).toBe(false)
          dispose()
        })
      })
    })
  })

  describe('to-do list (server-authoritative)', () => {
    // The chat store does not extract todos from message content — the
    // worker persists every event in agent_todos and ships the current
    // snapshot via ListAgentMessagesResponse.todos and the
    // AgentTodosChanged broadcast. These tests cover the
    // server-authoritative hydration path.

    const sampleTodos = [
      create(TodoItemSchema, { content: 'Write tests', status: TodoStatus.COMPLETED, activeForm: 'Writing tests' }),
      create(TodoItemSchema, { content: 'Deploy', status: TodoStatus.IN_PROGRESS, activeForm: 'Deploying' }),
    ]

    it('hydrates todos from ListAgentMessagesResponse.todos on cold start', async () => {
      await createRoot(async (dispose) => {
        const store = createChatStore()
        mockListAgentMessages.mockResolvedValueOnce({
          messages: [makeMessage('m1', 1n)],
          hasMore: false,
          todos: sampleTodos,
          todosLoaded: true,
        })

        await store.loadInitialMessages('w1', 'a1')
        expect(store.todos.get('a1')).toEqual([
          { id: undefined, content: 'Write tests', status: 'completed', activeForm: 'Writing tests', description: undefined },
          { id: undefined, content: 'Deploy', status: 'in_progress', activeForm: 'Deploying', description: undefined },
        ])
        dispose()
      })
    })

    it('treats an empty todos response as authoritative ("agent has none") when todosLoaded', async () => {
      await createRoot(async (dispose) => {
        const store = createChatStore()
        // Seed a non-empty list, then deliver an authoritative empty snapshot
        // (todosLoaded true): it overwrites to empty.
        store.todos.replace('a1', sampleTodos)
        mockListAgentMessages.mockResolvedValueOnce({
          messages: [makeMessage('m1', 1n)],
          hasMore: false,
          todos: [],
          todosLoaded: true,
        })

        await store.loadInitialMessages('w1', 'a1')
        expect(store.todos.get('a1')).toEqual([])
        dispose()
      })
    })

    it('leaves an existing list intact when todos failed to load (empty + todosLoaded false)', async () => {
      await createRoot(async (dispose) => {
        const store = createChatStore()
        // A populated list (e.g. from a prior AgentTodosChanged broadcast).
        store.todos.replace('a1', sampleTodos)
        // jump-to-latest whose backend LoadTodos failed: todos=[] but NOT loaded.
        // Must NOT wipe the populated list (the wire can't otherwise distinguish a
        // failed load from a genuine empty list).
        mockListAgentMessages.mockResolvedValueOnce({
          messages: [makeMessage('m1', 1n)],
          hasMore: false,
          todos: [],
          todosLoaded: false,
        })

        await store.loadInitialMessages('w1', 'a1')
        expect(store.todos.get('a1')).toHaveLength(2)
        dispose()
      })
    })

    it('addMessage does not mutate todos (broadcasts drive the list)', () => {
      createRoot((dispose) => {
        const store = createChatStore()
        store.addMessage('a1', makeTodoWriteMessage('m1', 1n, [
          { content: 'Write tests', status: 'completed', activeForm: 'Writing tests' },
        ]))
        expect(store.todos.get('a1')).toEqual([])
        dispose()
      })
    })

    it('todos.replace applies the wire payload wholesale', () => {
      createRoot((dispose) => {
        const store = createChatStore()
        store.todos.replace('a1', sampleTodos)
        expect(store.todos.get('a1')).toHaveLength(2)
        // A subsequent broadcast wins, including a clear.
        store.todos.replace('a1', [])
        expect(store.todos.get('a1')).toEqual([])
        dispose()
      })
    })

    it('returns empty array for an agent with no todos', () => {
      createRoot((dispose) => {
        const store = createChatStore()
        expect(store.todos.get('a1')).toEqual([])
        dispose()
      })
    })
  })

  describe('local (optimistic) messages with seq === 0n', () => {
    it('should append local message at the end', () => {
      createRoot((dispose) => {
        const store = createChatStore()
        store.addMessage('a1', makeMessage('s1', 1n))
        store.addMessage('a1', makeMessage('s2', 2n))
        store.addMessage('a1', makeMessage('local1', 0n))

        const msgs = store.getMessages('a1')
        expect(msgs).toHaveLength(3)
        expect(msgs[0].id).toBe('s1')
        expect(msgs[1].id).toBe('s2')
        expect(msgs[2].id).toBe('local1')
        dispose()
      })
    })

    it('should insert server message before trailing local messages', () => {
      createRoot((dispose) => {
        const store = createChatStore()
        store.addMessage('a1', makeMessage('s1', 1n))
        store.addMessage('a1', makeMessage('local1', 0n))

        // Server message arrives — should go before local1
        store.addMessage('a1', makeMessage('s2', 2n))

        const msgs = store.getMessages('a1')
        expect(msgs).toHaveLength(3)
        expect(msgs[0].id).toBe('s1')
        expect(msgs[1].id).toBe('s2')
        expect(msgs[2].id).toBe('local1')
        dispose()
      })
    })

    it('should keep multiple local messages at end in insertion order', () => {
      createRoot((dispose) => {
        const store = createChatStore()
        store.addMessage('a1', makeMessage('s1', 1n))
        store.addMessage('a1', makeMessage('local1', 0n))
        store.addMessage('a1', makeMessage('local2', 0n))

        // Server message arrives
        store.addMessage('a1', makeMessage('s2', 2n))

        const msgs = store.getMessages('a1')
        expect(msgs).toHaveLength(4)
        expect(msgs[0].id).toBe('s1')
        expect(msgs[1].id).toBe('s2')
        expect(msgs[2].id).toBe('local1')
        expect(msgs[3].id).toBe('local2')
        dispose()
      })
    })

    it('getLastSeq should skip trailing local messages', () => {
      createRoot((dispose) => {
        const store = createChatStore()
        store.addMessage('a1', makeMessage('s1', 5n))
        store.addMessage('a1', makeMessage('s2', 10n))
        store.addMessage('a1', makeMessage('local1', 0n))

        expect(store.getLastSeq('a1')).toBe(10n)
        dispose()
      })
    })

    it('getLastSeq should return 0n when only local messages exist', () => {
      createRoot((dispose) => {
        const store = createChatStore()
        store.addMessage('a1', makeMessage('local1', 0n))

        expect(store.getLastSeq('a1')).toBe(0n)
        dispose()
      })
    })

    it('getFirstSeq should return first server message seq', () => {
      createRoot((dispose) => {
        const store = createChatStore()
        store.setMessages('a1', [makeMessage('s1', 5n), makeMessage('s2', 6n)])
        expect(store.getFirstSeq('a1')).toBe(5n)
        dispose()
      })
    })
  })

  describe('messageVersion', () => {
    it('should return 0 for unknown agent', () => {
      createRoot((dispose) => {
        const store = createChatStore()
        expect(store.getMessageVersion('unknown')).toBe(0)
        dispose()
      })
    })

    it('should increment on addMessage with a new message', () => {
      createRoot((dispose) => {
        const store = createChatStore()
        expect(store.getMessageVersion('a1')).toBe(0)

        store.addMessage('a1', makeMessage('m1', 1n))
        expect(store.getMessageVersion('a1')).toBe(1)

        store.addMessage('a1', makeMessage('m2', 2n))
        expect(store.getMessageVersion('a1')).toBe(2)
        dispose()
      })
    })

    it('should increment on thread merge (same ID, updated content)', () => {
      createRoot((dispose) => {
        const store = createChatStore()
        store.addMessage('a1', makeMessage('m1', 1n))
        expect(store.getMessageVersion('a1')).toBe(1)

        // Thread merge: same ID as m1, bumped seq
        store.addMessage('a1', makeMessage('m1', 3n))
        expect(store.getMessageVersion('a1')).toBe(2)
        dispose()
      })
    })

    it('should not increment on setMessages', () => {
      createRoot((dispose) => {
        const store = createChatStore()
        store.setMessages('a1', [makeMessage('m1', 1n), makeMessage('m2', 2n)])
        expect(store.getMessageVersion('a1')).toBe(0)
        dispose()
      })
    })

    it('should track versions independently per agent', () => {
      createRoot((dispose) => {
        const store = createChatStore()
        store.addMessage('a1', makeMessage('m1', 1n))
        store.addMessage('a2', makeMessage('m2', 2n))
        store.addMessage('a1', makeMessage('m3', 3n))

        expect(store.getMessageVersion('a1')).toBe(2)
        expect(store.getMessageVersion('a2')).toBe(1)
        dispose()
      })
    })
  })
})
