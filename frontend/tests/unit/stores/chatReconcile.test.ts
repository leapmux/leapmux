import { create } from '@bufbuild/protobuf'
import { describe, expect, it } from 'vitest'
import { AgentChatMessageSchema, ContentCompression, MessageSource } from '~/generated/leapmux/v1/agent_pb'
import { isOptimisticLocal, isReconcilableLocal, priorServerIds, reconcileEchoedLocals, userMessageSignature } from '~/stores/chatReconcile'

function userMsg(id: string, seq: bigint, content: string, deliveryError = '') {
  return create(AgentChatMessageSchema, {
    id,
    source: MessageSource.USER,
    content: new TextEncoder().encode(JSON.stringify({ content })),
    contentCompression: ContentCompression.NONE,
    seq,
    deliveryError,
  })
}

function agentMsg(id: string, seq: bigint) {
  return create(AgentChatMessageSchema, {
    id,
    source: MessageSource.AGENT,
    content: new TextEncoder().encode(JSON.stringify({ content: 'hi' })),
    contentCompression: ContentCompression.NONE,
    seq,
  })
}

/** A user message whose content JSON is an arbitrary wire shape. */
function rawUserMsg(id: string, seq: bigint, contentObj: unknown) {
  return create(AgentChatMessageSchema, {
    id,
    source: MessageSource.USER,
    content: new TextEncoder().encode(JSON.stringify(contentObj)),
    contentCompression: ContentCompression.NONE,
    seq,
  })
}

describe('chatReconcile', () => {
  describe('userMessageSignature', () => {
    it('is null for a non-user message', () => {
      expect(userMessageSignature(agentMsg('a1', 1n))).toBeNull()
    })

    it('is stable across two messages with identical content', () => {
      expect(userMessageSignature(userMsg('local-x', 0n, 'hello'))).toBe(userMessageSignature(userMsg('server-x', 5n, 'hello')))
    })

    it('differs for different content', () => {
      expect(userMessageSignature(userMsg('a', 0n, 'hello'))).not.toBe(userMessageSignature(userMsg('b', 0n, 'world')))
    })

    it('matches a block-array content echo to a plain-string local', () => {
      // An Anthropic-style echo whose content is `[{type:'text',text}]` must sign
      // the same as the optimistic local's plain string so they reconcile.
      const local = userMsg('local-x', 0n, 'hello world')
      const echo = rawUserMsg('server-x', 5n, { content: [{ type: 'text', text: 'hello world' }] })
      expect(userMessageSignature(echo)).toBe(userMessageSignature(local))
    })

    it('joins only the text blocks of a mixed block-array content', () => {
      // An echo whose content interleaves a non-text block must still normalize
      // to the local's plain string (image/other blocks are dropped).
      const local = userMsg('local-x', 0n, 'look here')
      const echo = rawUserMsg('server-x', 5n, {
        content: [{ type: 'image', source: {} }, { type: 'text', text: 'look here' }],
      })
      expect(userMessageSignature(echo)).toBe(userMessageSignature(local))
    })

    it('includes flat attachments in the signature (a no-attachment echo differs)', () => {
      // LeapMux persists a user message flat as { content, attachments }. The
      // attachment metadata is part of the signature, so an echo carrying the same
      // attachments reconciles while one without them does not.
      const local = rawUserMsg('local-x', 0n, {
        content: 'see this',
        attachments: [{ filename: 'a.png', mime_type: 'image/png' }],
      })
      const echo = rawUserMsg('server-x', 5n, {
        content: 'see this',
        attachments: [{ filename: 'a.png', mime_type: 'image/png' }],
      })
      expect(userMessageSignature(echo)).toBe(userMessageSignature(local))
      const echoNoAtt = rawUserMsg('server-y', 6n, { content: 'see this' })
      expect(userMessageSignature(echoNoAtt)).not.toBe(userMessageSignature(local))
    })

    it('is null for a block-array content with no text blocks (image-only)', () => {
      const echo = rawUserMsg('server-x', 5n, { content: [{ type: 'image', source: {} }] })
      expect(userMessageSignature(echo)).toBeNull()
    })
  })

  describe('isOptimisticLocal', () => {
    it('is true for any seq-0n message, regardless of id / source / delivery state', () => {
      expect(isOptimisticLocal(userMsg('local-1', 0n, 'hi'))).toBe(true)
      // Looser than isReconcilableLocal: a FAILED local and a non-`local-` id are
      // still optimistic locals (pinned to the tail) -- only the seq matters.
      expect(isOptimisticLocal(userMsg('local-1', 0n, 'hi', 'failed to deliver'))).toBe(true)
      expect(isOptimisticLocal(userMsg('other-1', 0n, 'hi'))).toBe(true)
    })

    it('is false once a real (non-zero) seq is assigned', () => {
      expect(isOptimisticLocal(userMsg('local-1', 7n, 'hi'))).toBe(false)
    })
  })

  describe('priorServerIds', () => {
    it('collects only the non-optimistic-local (server) row ids', () => {
      const ids = priorServerIds([
        agentMsg('a1', 1n),
        userMsg('local-1', 0n, 'hi'), // optimistic local -> excluded
        userMsg('u1', 2n, 'sent'), // server echo of a sent user message -> included
      ])
      expect(ids).toEqual(new Set(['a1', 'u1']))
    })

    it('is empty for an empty window or an all-local window', () => {
      expect(priorServerIds([])).toEqual(new Set())
      expect(priorServerIds([userMsg('local-1', 0n, 'a'), userMsg('local-2', 0n, 'b')])).toEqual(new Set())
    })
  })

  describe('isReconcilableLocal', () => {
    it('is true for a pending optimistic local user message', () => {
      expect(isReconcilableLocal(userMsg('local-1', 0n, 'hi'))).toBe(true)
    })

    it('is true for a FAILED send (deliveryError set) -- an arriving echo reconciles it (delivery is truth)', () => {
      expect(isReconcilableLocal(userMsg('local-1', 0n, 'hi', 'failed to deliver'))).toBe(true)
    })

    it('is false once a real seq is assigned', () => {
      expect(isReconcilableLocal(userMsg('local-1', 7n, 'hi'))).toBe(false)
    })

    it('is false for a non-local id', () => {
      expect(isReconcilableLocal(userMsg('server-1', 0n, 'hi'))).toBe(false)
    })
  })

  describe('reconcileEchoedLocals', () => {
    it('returns an empty set when there are no locals', () => {
      expect(reconcileEchoedLocals('a1', [userMsg('s1', 5n, 'hi')], []).size).toBe(0)
    })

    it('reconciles a local whose signature the page echoes', () => {
      const reconciled = reconcileEchoedLocals('a1', [userMsg('s1', 5n, 'hi')], [userMsg('local-1', 0n, 'hi')])
      expect([...reconciled]).toEqual(['local-1'])
    })

    it('consumes one echo per local: two identical locals + one echo reconciles exactly one', () => {
      const reconciled = reconcileEchoedLocals(
        'a1',
        [userMsg('s1', 5n, 'dup')],
        [userMsg('local-1', 0n, 'dup'), userMsg('local-2', 0n, 'dup')],
      )
      expect(reconciled.size).toBe(1)
      // The first matching local is consumed; the second stays pending.
      expect(reconciled.has('local-1')).toBe(true)
      expect(reconciled.has('local-2')).toBe(false)
    })

    it('reconciles both identical locals when both echoes are present', () => {
      const reconciled = reconcileEchoedLocals(
        'a1',
        [userMsg('s1', 5n, 'dup'), userMsg('s2', 6n, 'dup')],
        [userMsg('local-1', 0n, 'dup'), userMsg('local-2', 0n, 'dup')],
      )
      expect(reconciled.size).toBe(2)
    })

    it('reconciles a FAILED local when the page echoes its text (delivery is truth)', () => {
      // A failed send IS reconcilable now: an echo of its text proves it was delivered,
      // so the failed bubble reconciles to the echo (its error is reclaimed by the store).
      const locals = [userMsg('local-1', 0n, 'hi', 'failed')].filter(isReconcilableLocal)
      expect([...reconcileEchoedLocals('a1', [userMsg('s1', 5n, 'hi')], locals)]).toEqual(['local-1'])
    })

    it('consumes an echo against a pending local before a same-text failed one', () => {
      // A failed "hi" (proto deliveryError) and a later pending "hi" retry, with ONE echo:
      // the pending one (awaiting an echo) must reconcile, NOT the failed one -- otherwise
      // the pending send is stranded. The failed bubble survives (its own echo never came).
      const reconciled = reconcileEchoedLocals(
        'a1',
        [userMsg('s1', 5n, 'hi')],
        [userMsg('local-failed', 0n, 'hi', 'failed'), userMsg('local-pending', 0n, 'hi')],
      )
      expect([...reconciled]).toEqual(['local-pending'])
    })

    it('does not consume an echo already standing as a server row for a still-pending duplicate', () => {
      // Two identical sends -> local-1 reconciled LIVE (its echo s1 is now a server
      // row in the window); local-2 still pending. A full-window replace whose page
      // re-lists s1 (but NOT local-2's own echo yet) must NOT reconcile local-2
      // against s1 -- that echo is already paired. Without the discount, local-2
      // would vanish until its real echo lands.
      const reconciled = reconcileEchoedLocals(
        'a1',
        [userMsg('s1', 5n, 'dup')],
        [userMsg('local-2', 0n, 'dup')],
        new Set(['s1']), // s1 is already a server row in the window
      )
      expect(reconciled.size).toBe(0)
    })

    it('reconciles the pending duplicate against the page echo that is NOT already present', () => {
      // Same setup, but the page now also carries local-2's own echo (s2). s1 is
      // discounted (already present), leaving s2 to reconcile local-2.
      const reconciled = reconcileEchoedLocals(
        'a1',
        [userMsg('s1', 5n, 'dup'), userMsg('s2', 6n, 'dup')],
        [userMsg('local-2', 0n, 'dup')],
        new Set(['s1']),
      )
      expect([...reconciled]).toEqual(['local-2'])
    })

    it('still reconciles a first send when an unrelated older server row shares its text', () => {
      // An older "ok" already sits in the window (e_old). The user sends "ok"
      // (local-1) and a jump-to-latest page carries BOTH e_old and the new echo e1.
      // e_old is discounted; e1 reconciles local-1 (no false under-reconcile).
      const reconciled = reconcileEchoedLocals(
        'a1',
        [userMsg('e_old', 3n, 'ok'), userMsg('e1', 9n, 'ok')],
        [userMsg('local-1', 0n, 'ok')],
        new Set(['e_old']),
      )
      expect([...reconciled]).toEqual(['local-1'])
    })
  })
})
