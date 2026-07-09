import type { AgentChatMessage } from '~/generated/leapmux/v1/agent_pb'
import { create } from '@bufbuild/protobuf'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { AgentChatMessageSchema, AgentProvider, ContentCompression, MessageSource } from '~/generated/leapmux/v1/agent_pb'
import { __resetMarkPreviewCacheForTest, forgetMarkPreview, getCachedMarkPreview, messageMarkPreviewText, warmMarkPreview } from './chatMarkPreview'
import * as registry from './providers/registry'
// Register provider plugins so messageMarkPreviewText (called inside warm) can resolve one.
import '~/components/chat/providers'

/** Force the resolved plugin's previewText to throw, to exercise the extraction guard. */
function withThrowingPlugin(body: () => void): void {
  const spy = vi.spyOn(registry, 'pluginFor').mockReturnValue({
    previewText() { throw new Error('boom') },
  } as unknown as ReturnType<typeof registry.pluginFor>)
  try {
    body()
  }
  finally {
    spy.mockRestore()
  }
}

function userMessage(seq: bigint, text: string): AgentChatMessage {
  return create(AgentChatMessageSchema, {
    id: `m${seq}`,
    source: MessageSource.USER,
    content: new TextEncoder().encode(JSON.stringify({ content: text })),
    contentCompression: ContentCompression.NONE,
    seq,
    agentProvider: AgentProvider.CLAUDE_CODE,
  })
}

/** A marked message carrying an arbitrary content shape (default provider Claude). */
function messageOf(content: Record<string, unknown>, agentProvider = AgentProvider.CLAUDE_CODE): AgentChatMessage {
  return create(AgentChatMessageSchema, {
    id: 'm1',
    source: MessageSource.USER,
    content: new TextEncoder().encode(JSON.stringify(content)),
    contentCompression: ContentCompression.NONE,
    seq: 5n,
    agentProvider,
  })
}

afterEach(() => __resetMarkPreviewCacheForTest())

describe('message mark preview text', () => {
  it('resolves a user message through its provider plugin and the shared default', () => {
    expect(messageMarkPreviewText(messageOf({ content: 'jump here' }))).toBe('jump here')
  })

  it('resolves a persisted Codex control-response row to its decision label', () => {
    // A structured {isSynthetic, controlResponse} row resolves through the plugin's
    // controlResponseDisplay -- Codex maps result.decision "accept" to "Allow".
    const row = messageOf({
      isSynthetic: true,
      controlResponse: {
        provider: 'CODEX',
        requestId: '7',
        request: { method: 'item/commandExecution/requestApproval' },
        response: { jsonrpc: '2.0', id: 7, result: { decision: 'accept' } },
      },
    }, AgentProvider.CODEX)
    expect(messageMarkPreviewText(row)).toBe('Allow')
  })

  it('resolves a Claude deny-with-feedback row to the "Sent feedback:" preview', () => {
    const row = messageOf({
      isSynthetic: true,
      controlResponse: {
        provider: 'CLAUDE_CODE',
        requestId: 'r',
        request: { request: { tool_name: 'Bash' } },
        response: { type: 'control_response', response: { request_id: 'r', response: { behavior: 'deny', message: 'use ripgrep instead' } } },
      },
    })
    expect(messageMarkPreviewText(row)).toBe('Sent feedback:\nuse ripgrep instead')
  })

  it('degrades a malformed control-response row to the generic label', () => {
    // Neither a recognizable decision nor a behavior envelope -> the fallback ladder's terminal.
    const row = messageOf({
      isSynthetic: true,
      controlResponse: { provider: 'CODEX', response: {} },
    }, AgentProvider.CODEX)
    expect(messageMarkPreviewText(row)).toBe('Responded')
  })

  it('returns null for a message with no previewable text', () => {
    expect(messageMarkPreviewText(messageOf({ type: 'assistant', message: { content: [{ type: 'text', text: 'hi' }] } }))).toBeNull()
  })

  it('degrades to null instead of propagating when a provider plugin previewText throws', () => {
    // A malformed message a plugin's previewText (or the parse it drives) chokes on must read
    // as "no preview", not blow up the hover effect / poison the async fetch as transient.
    withThrowingPlugin(() => {
      expect(messageMarkPreviewText(messageOf({ content: 'x' }))).toBeNull()
    })
  })
})

describe('warmmarkpreview', () => {
  it('resolves from the loaded window without fetching', () => {
    const fetchMessageBySeq = vi.fn()
    warmMarkPreview('w1', 'a1', 5n, {
      getLoadedMessageBySeq: () => userMessage(5n, 'in window'),
      fetchMessageBySeq,
    })
    expect(getCachedMarkPreview('a1', 5n)).toBe('in window')
    expect(fetchMessageBySeq).not.toHaveBeenCalled()
  })

  it('fetches an out-of-window mark and caches its extracted preview', async () => {
    const fetchMessageBySeq = vi.fn().mockResolvedValue(userMessage(9n, 'far away'))
    warmMarkPreview('w1', 'a1', 9n, { getLoadedMessageBySeq: () => undefined, fetchMessageBySeq })
    // Undefined = still resolving; the tooltip shows a loading line meanwhile.
    expect(getCachedMarkPreview('a1', 9n)).toBeUndefined()
    await vi.waitFor(() => expect(getCachedMarkPreview('a1', 9n)).toBe('far away'))
    expect(fetchMessageBySeq).toHaveBeenCalledOnce()
  })

  it('dedupes concurrent hovers on the same seq to a single fetch', async () => {
    let resolve!: (m: AgentChatMessage) => void
    const pending = new Promise<AgentChatMessage>((r) => {
      resolve = r
    })
    const fetchMessageBySeq = vi.fn().mockReturnValue(pending)
    const deps = { getLoadedMessageBySeq: () => undefined, fetchMessageBySeq }
    warmMarkPreview('w1', 'a1', 9n, deps)
    warmMarkPreview('w1', 'a1', 9n, deps)
    expect(fetchMessageBySeq).toHaveBeenCalledOnce()
    resolve(userMessage(9n, 'landed'))
    await vi.waitFor(() => expect(getCachedMarkPreview('a1', 9n)).toBe('landed'))
  })

  it('does not re-fetch a seq already resolved', async () => {
    const fetchMessageBySeq = vi.fn().mockResolvedValue(userMessage(9n, 'once'))
    const deps = { getLoadedMessageBySeq: () => undefined, fetchMessageBySeq }
    warmMarkPreview('w1', 'a1', 9n, deps)
    await vi.waitFor(() => expect(getCachedMarkPreview('a1', 9n)).toBe('once'))
    warmMarkPreview('w1', 'a1', 9n, deps)
    expect(fetchMessageBySeq).toHaveBeenCalledOnce()
  })

  it('bounds the per-agent preview cache across long hover sessions', () => {
    for (let seq = 1n; seq <= 505n; seq++) {
      warmMarkPreview('w1', 'a1', seq, {
        getLoadedMessageBySeq: () => userMessage(seq, `message ${seq}`),
        fetchMessageBySeq: vi.fn(),
      })
    }
    expect(getCachedMarkPreview('a1', 1n)).toBeUndefined()
    expect(getCachedMarkPreview('a1', 505n)).toBe('message 505')
  })

  it('caches "" when the message is gone, so the rail shows a label without re-fetching', async () => {
    const fetchMessageBySeq = vi.fn().mockResolvedValue(undefined)
    const deps = { getLoadedMessageBySeq: () => undefined, fetchMessageBySeq }
    warmMarkPreview('w1', 'a1', 9n, deps)
    await vi.waitFor(() => expect(getCachedMarkPreview('a1', 9n)).toBe(''))
    warmMarkPreview('w1', 'a1', 9n, deps)
    expect(fetchMessageBySeq).toHaveBeenCalledOnce()
  })

  it('leaves the cache unresolved when the fetch rejects, so a later hover retries', async () => {
    // A transient RPC failure (reject) must NOT be cached as '' -- that would poison the
    // dot with a permanent label for the rest of the session. It stays unresolved and a
    // later hover re-fetches; only a resolved-undefined (definitive absence) caches ''.
    const fetchMessageBySeq = vi.fn()
      .mockRejectedValueOnce(new Error('boom'))
      .mockResolvedValueOnce(userMessage(9n, 'recovered'))
    const deps = { getLoadedMessageBySeq: () => undefined, fetchMessageBySeq }
    warmMarkPreview('w1', 'a1', 9n, deps)
    // The rejection settles and the in-flight token is dropped, with nothing cached.
    await vi.waitFor(() => expect(fetchMessageBySeq).toHaveBeenCalledOnce())
    await Promise.resolve()
    await Promise.resolve()
    expect(getCachedMarkPreview('a1', 9n)).toBeUndefined()
    // A later hover re-fetches (the '' poison is gone) and resolves the real preview.
    warmMarkPreview('w1', 'a1', 9n, deps)
    await vi.waitFor(() => expect(getCachedMarkPreview('a1', 9n)).toBe('recovered'))
    expect(fetchMessageBySeq).toHaveBeenCalledTimes(2)
  })

  it('loaded-window extraction throw caches "" synchronously without propagating', () => {
    // A plugin previewText throw on the SYNC loaded-window path must not escape into the
    // caller's hover effect; it caches '' (a label), same as any un-previewable message.
    withThrowingPlugin(() => {
      const fetchMessageBySeq = vi.fn()
      expect(() =>
        warmMarkPreview('w1', 'a1', 4n, { getLoadedMessageBySeq: () => userMessage(4n, 'x'), fetchMessageBySeq }),
      ).not.toThrow()
      expect(getCachedMarkPreview('a1', 4n)).toBe('')
      expect(fetchMessageBySeq).not.toHaveBeenCalled()
    })
  })

  it('fetched-message extraction throw caches "" (a real absence), NOT an endless re-fetch', async () => {
    // The fetch SUCCEEDS but extraction throws: this is a permanent parse failure, so cache
    // '' once (a label) rather than misreading it as the transient RPC failure the .catch
    // handles -- which would re-fetch the same dot on every hover for the session. The spy is
    // held through the await (not withThrowingPlugin, which would restore before the .then).
    const spy = vi.spyOn(registry, 'pluginFor').mockReturnValue({
      previewText() { throw new Error('boom') },
    } as unknown as ReturnType<typeof registry.pluginFor>)
    try {
      const fetchMessageBySeq = vi.fn().mockResolvedValue(userMessage(9n, 'far'))
      const deps = { getLoadedMessageBySeq: () => undefined, fetchMessageBySeq }
      warmMarkPreview('w1', 'a1', 9n, deps)
      await vi.waitFor(() => expect(getCachedMarkPreview('a1', 9n)).toBe(''))
      warmMarkPreview('w1', 'a1', 9n, deps) // a second hover must NOT re-fetch
      expect(fetchMessageBySeq).toHaveBeenCalledOnce()
    }
    finally {
      spy.mockRestore()
    }
  })

  it('ignores optimistic locals (seq 0n)', () => {
    const fetchMessageBySeq = vi.fn()
    warmMarkPreview('w1', 'a1', 0n, { getLoadedMessageBySeq: () => undefined, fetchMessageBySeq })
    expect(fetchMessageBySeq).not.toHaveBeenCalled()
    expect(getCachedMarkPreview('a1', 0n)).toBeUndefined()
  })

  it('forgetMarkPreview drops one agent\'s entries and leaves other agents intact', () => {
    warmMarkPreview('w1', 'a1', 5n, { getLoadedMessageBySeq: () => userMessage(5n, 'agent one'), fetchMessageBySeq: vi.fn() })
    warmMarkPreview('w1', 'a2', 5n, { getLoadedMessageBySeq: () => userMessage(5n, 'agent two'), fetchMessageBySeq: vi.fn() })
    expect(getCachedMarkPreview('a1', 5n)).toBe('agent one')
    expect(getCachedMarkPreview('a2', 5n)).toBe('agent two')

    forgetMarkPreview('a1')
    expect(getCachedMarkPreview('a1', 5n)).toBeUndefined() // pruned
    expect(getCachedMarkPreview('a2', 5n)).toBe('agent two') // untouched
  })

  it('fences an in-flight fetch when the agent is forgotten mid-flight (no re-leak)', async () => {
    let resolve!: (m: AgentChatMessage | undefined) => void
    const pending = new Promise<AgentChatMessage | undefined>((r) => {
      resolve = r
    })
    const fetchMessageBySeq = vi.fn().mockReturnValue(pending)
    warmMarkPreview('w1', 'a1', 9n, { getLoadedMessageBySeq: () => undefined, fetchMessageBySeq })
    expect(getCachedMarkPreview('a1', 9n)).toBeUndefined() // still resolving

    // Agent closed while the fetch is in flight: the cache has no entry yet to prune.
    forgetMarkPreview('a1')
    // The fetch resolves AFTER forget -- it must NOT write an entry back for the closed agent.
    resolve(userMessage(9n, 'landed too late'))
    await Promise.resolve()
    await Promise.resolve()
    expect(getCachedMarkPreview('a1', 9n)).toBeUndefined()
  })

  it('a re-warm after forget supersedes an older in-flight fetch (no stale clobber)', async () => {
    let resolveOld!: (m: AgentChatMessage) => void
    const oldPending = new Promise<AgentChatMessage>((r) => {
      resolveOld = r
    })
    const oldFetch = vi.fn().mockReturnValue(oldPending)
    warmMarkPreview('w1', 'a1', 9n, { getLoadedMessageBySeq: () => undefined, fetchMessageBySeq: oldFetch })

    forgetMarkPreview('a1') // close
    // Reopen + re-warm: a fresh fetch resolves first with the current value.
    const newFetch = vi.fn().mockResolvedValue(userMessage(9n, 'fresh'))
    warmMarkPreview('w1', 'a1', 9n, { getLoadedMessageBySeq: () => undefined, fetchMessageBySeq: newFetch })
    await vi.waitFor(() => expect(getCachedMarkPreview('a1', 9n)).toBe('fresh'))

    // The OLD fetch resolves late; its token was superseded, so it must not clobber 'fresh'.
    resolveOld(userMessage(9n, 'stale'))
    await Promise.resolve()
    await Promise.resolve()
    expect(getCachedMarkPreview('a1', 9n)).toBe('fresh')
  })

  it('re-fetches after forget so a stale "" does not survive a close/reopen', async () => {
    // First warm resolves empty (message transiently gone) and caches ''.
    const gone = vi.fn().mockResolvedValue(undefined)
    warmMarkPreview('w1', 'a1', 9n, { getLoadedMessageBySeq: () => undefined, fetchMessageBySeq: gone })
    await vi.waitFor(() => expect(getCachedMarkPreview('a1', 9n)).toBe(''))

    // Agent closed -> cache pruned. The reopened agent now has the message, and a re-warm
    // must fetch again rather than short-circuit on the stale ''.
    forgetMarkPreview('a1')
    expect(getCachedMarkPreview('a1', 9n)).toBeUndefined()
    const back = vi.fn().mockResolvedValue(userMessage(9n, 'now available'))
    warmMarkPreview('w1', 'a1', 9n, { getLoadedMessageBySeq: () => undefined, fetchMessageBySeq: back })
    await vi.waitFor(() => expect(getCachedMarkPreview('a1', 9n)).toBe('now available'))
    expect(back).toHaveBeenCalledOnce()
  })
})
