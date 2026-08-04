import type { UserCrdtState } from '~/generated/leapmux/v1/user_crdt_pb'
import type { CRDTBridge } from '~/lib/crdt'
import { create } from '@bufbuild/protobuf'
import { describe, expect, it } from 'vitest'
import { UserCrdtStateSchema } from '~/generated/leapmux/v1/user_crdt_pb'
import { ctxFromBridge, HLCClock, withBridgeAndState } from '~/lib/crdt'

/**
 * `withBridgeAndState` is the wiring-readiness preamble every CRDT-
 * emitting op-builder in the layout / floating-window stores relies
 * on. The contract has one failure mode -- speculativeState hasn't
 * landed yet -- and one happy path; these tests pin both so a future
 * change to the bridge interface or the helper's signature can't
 * silently bypass the guard.
 */
describe('withBridgeAndState', () => {
  function makeBridge(overrides: { state?: UserCrdtState | null } = {}): CRDTBridge {
    const state = overrides.state === undefined ? create(UserCrdtStateSchema, { userId: 'user-1' }) : overrides.state
    const clock = new HLCClock('client-1')
    return {
      workspaceId: () => 'ws-1',
      enqueue: batch => batch.batchId,
      flushNow: () => {},
      clock: () => clock,
      originClientId: () => 'origin-1',
      speculativeState: () => state,
    }
  }

  it('happy path: invokes fn(ctx, state) and returns its result', () => {
    const bridge = makeBridge()
    const result = withBridgeAndState(bridge, (ctx, state) => {
      expect(ctx.originClientId).toBe('origin-1')
      expect(ctx.clock).toBeInstanceOf(HLCClock)
      expect(state.userId).toBe('user-1')
      return 'ok' as const
    }, null)
    expect(result).toBe('ok')
  })

  it('returns fallback when bridge.speculativeState() returns null', () => {
    const bridge = makeBridge({ state: null })
    let called = false
    const result = withBridgeAndState<string | number>(bridge, () => {
      called = true
      return 'should-not-run'
    }, 42)
    expect(called).toBe(false)
    expect(result).toBe(42)
  })

  it('propagates exceptions thrown from fn (does not swallow)', () => {
    const bridge = makeBridge()
    expect(() => withBridgeAndState(bridge, () => {
      throw new Error('boom')
    }, null)).toThrow('boom')
  })

  it('ctx threads through the bridge\'s originClientId verbatim', () => {
    // Side-channel: the helper must pass originClientId from
    // `bridge.originClientId()` into the OpBuilderCtx so the op-id
    // / origin tracking flows through unchanged for downstream
    // op-emitters.
    const bridge: CRDTBridge = {
      workspaceId: () => 'ws-2',
      enqueue: batch => batch.batchId,
      flushNow: () => {},
      clock: () => new HLCClock('client-2'),
      originClientId: () => 'unique-origin-xyz',
      speculativeState: () => create(UserCrdtStateSchema, { userId: 'user-2' }),
    }
    let captured: string | null = null
    withBridgeAndState(bridge, (ctx) => {
      captured = ctx.originClientId
    }, undefined as void)
    expect(captured).toBe('unique-origin-xyz')
  })
})

describe('ctxFromBridge', () => {
  // `ctxFromBridge` is total -- a wired bridge always carries both fields --
  // and is called directly from sites that don't need state (e.g. the
  // `emitUpdateRatios` / `emitUpdatePosition` single-op writes). What matters
  // is that it reads both fields off the bridge it was handed, rather than
  // any ambient/global bridge.
  function makeBridge(clock: HLCClock): CRDTBridge {
    return {
      workspaceId: () => 'ws',
      enqueue: batch => batch.batchId,
      flushNow: () => {},
      clock: () => clock,
      originClientId: () => 'origin',
      speculativeState: () => null,
    }
  }

  it('threads the bridge\'s own clock and origin into the ctx', () => {
    const clock = new HLCClock('client')
    const ctx = ctxFromBridge(makeBridge(clock))
    expect(ctx.originClientId).toBe('origin')
    expect(ctx.clock).toBe(clock)
  })
})
