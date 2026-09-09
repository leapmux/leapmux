import type { Page } from '@playwright/test'
import { describe, expect, it, vi } from 'vitest'
import { applySimulatedSafeArea, IPHONE_PORTRAIT, ZERO_INSETS } from './safeArea'

/**
 * A `Page` that records what reaches the CDP session.
 *
 * The helper is one CDP call, so the payload IS its behaviour: a spec that
 * measures against these insets proves nothing if the command never carried
 * them.
 */
interface InsetsPayload {
  insets: Record<string, number>
}

function fakePage() {
  const send = vi.fn(async (_method: string, _params: InsetsPayload) => {})
  const page = {
    context: () => ({ newCDPSession: async () => ({ send }) }),
  } as unknown as Page
  return { page, send }
}

describe('applySimulatedSafeArea', () => {
  it('sends the insets as the safe-area override', async () => {
    const { page, send } = fakePage()

    await applySimulatedSafeArea(page, IPHONE_PORTRAIT)

    expect(send).toHaveBeenCalledTimes(1)
    expect(send).toHaveBeenCalledWith('Emulation.setSafeAreaInsetsOverride', {
      insets: { top: 47, right: 0, bottom: 34, left: 0 },
    })
  })

  // The documented contract, and the reason the helper spells every edge out
  // rather than spreading the caller's object: an OMITTED key leaves that
  // variable undefined, so a previous override would survive into the next
  // measurement and the spec would read someone else's geometry.
  it('states all four edges even when every one is zero', async () => {
    const { page, send } = fakePage()

    await applySimulatedSafeArea(page, ZERO_INSETS)

    const payload = send.mock.calls[0]![1]
    expect(Object.keys(payload.insets).sort()).toEqual(['bottom', 'left', 'right', 'top'])
    expect(payload.insets).toEqual({ top: 0, right: 0, bottom: 0, left: 0 })
  })

  it('opens one session for each call, so a later override replaces the earlier one', async () => {
    const { page, send } = fakePage()

    await applySimulatedSafeArea(page, IPHONE_PORTRAIT)
    await applySimulatedSafeArea(page, ZERO_INSETS)

    expect(send).toHaveBeenCalledTimes(2)
    expect(send.mock.calls[1]![1]).toEqual({ insets: { top: 0, right: 0, bottom: 0, left: 0 } })
  })
})
