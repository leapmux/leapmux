import type { Page } from '@playwright/test'
import type { AgentSettledEventDetail } from '../../../src/lib/agentSettledEvent'
import { expect } from '@playwright/test'
import { AgentActivityState } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { AGENT_SETTLED_EVENT } from '../../../src/lib/agentSettledEvent'
import { isObject } from '../../../src/lib/jsonPick'
import { setInitialBrowserPref } from './ui'

const DOORBELL_SRC = 'benkirb-electronic-doorbell'

interface SoundProbeState {
  plays: string[]
  settled: unknown[]
  audioInstalled: boolean
  receiptInstalled: boolean
}

declare global {
  interface Window {
    __nativeSoundProbe?: SoundProbeState
  }
}

export interface SoundReceiptBoundary {
  agentId: string
  after: number
}

/** Validate the browser receipt before it can satisfy a quiet sound assertion. */
export function parseSettledReceipt(value: unknown): AgentSettledEventDetail {
  if (!isObject(value) || typeof value.agentId !== 'string' || value.agentId === '')
    throw new Error('The settled receipt needs an agent ID.')
  if (value.state !== AgentActivityState.IDLE && value.state !== AgentActivityState.WAITING_FOR_USER)
    throw new Error('The settled receipt needs an idle or waiting state.')
  const receipt: AgentSettledEventDetail = { agentId: value.agentId, state: value.state }
  if (Object.hasOwn(value, 'numToolUses')) {
    const count = value.numToolUses
    if (typeof count !== 'number' || !Number.isSafeInteger(count) || count < 0)
      throw new Error('The settled receipt tool count must be a nonnegative integer.')
    receipt.numToolUses = count
  }
  return receipt
}

/** Select only a new applied idle edge for the requested agent. */
export function selectIdleReceipt(receipts: readonly unknown[], boundary: SoundReceiptBoundary): AgentSettledEventDetail | undefined {
  if (!boundary.agentId || !Number.isSafeInteger(boundary.after) || boundary.after < 0)
    throw new Error('The sound receipt boundary needs an agent ID and nonnegative cursor.')
  return receipts.slice(boundary.after).map(parseSettledReceipt).find(receipt => receipt.agentId === boundary.agentId && receipt.state === AgentActivityState.IDLE)
}

function installBrowserSoundProbe(options: { eventName: string, spyAudio: boolean, reset: boolean }): void {
  const state = window.__nativeSoundProbe ?? { plays: [], settled: [], audioInstalled: false, receiptInstalled: false }
  if (options.reset) {
    state.plays = []
    state.settled = []
  }
  window.__nativeSoundProbe = state
  if (options.spyAudio && !state.audioInstalled) {
    state.audioInstalled = true
    HTMLAudioElement.prototype.play = function () {
      window.__nativeSoundProbe?.plays.push(this.src)
      return Promise.resolve()
    }
  }
  if (!state.receiptInstalled) {
    state.receiptInstalled = true
    window.addEventListener(options.eventName, (event) => {
      if (event instanceof CustomEvent)
        window.__nativeSoundProbe?.settled.push(event.detail)
    })
  }
}

/** Record sound calls and settled receipts before the app starts, then apply the preference. */
export async function armTurnEndSound(page: Page, userId: string, sound: 'ding-dong' | 'none'): Promise<void> {
  await page.addInitScript(installBrowserSoundProbe, { eventName: AGENT_SETTLED_EVENT, spyAudio: true, reset: true })
  await setInitialBrowserPref(page, userId, 'turnEndSound', sound)
  await page.reload()
}

async function soundProbeSnapshot(page: Page): Promise<{ plays: string[], settled: unknown[] }> {
  return page.evaluate(() => {
    const state = window.__nativeSoundProbe
    if (!state)
      throw new Error('The sound probe did not start before the app.')
    return { plays: [...state.plays], settled: [...state.settled] }
  })
}

/** Capture this cursor before the native turn that the assertion must observe. */
export async function soundReceiptCursor(page: Page): Promise<number> {
  return (await soundProbeSnapshot(page)).settled.length
}

/** Observe applied activity transitions without changing the sound preference or replay baseline. */
export async function observeSettledReceipts(page: Page): Promise<number> {
  await page.evaluate(installBrowserSoundProbe, { eventName: AGENT_SETTLED_EVENT, spyAudio: false, reset: false })
  return soundReceiptCursor(page)
}

/**
 * Read the applied idle receipt after the boundary, without waiting for one.
 * A receipt exists only for a settle edge: WORKING to any other state.
 * A move from WAITING_FOR_USER to IDLE is not such an edge, so it records nothing.
 */
export async function currentIdleReceipt(page: Page, boundary: SoundReceiptBoundary): Promise<AgentSettledEventDetail | undefined> {
  return selectIdleReceipt((await soundProbeSnapshot(page)).settled, boundary)
}

/** Wait until the browser applies the native idle edge and returns from its sound callback. */
export async function waitForIdleSoundReceipt(page: Page, boundary: SoundReceiptBoundary): Promise<AgentSettledEventDetail> {
  let receipt: AgentSettledEventDetail | undefined
  await expect.poll(async () => {
    receipt = await currentIdleReceipt(page, boundary)
    return receipt !== undefined
  }).toBe(true)
  if (!receipt)
    throw new Error('The native turn produced no applied idle receipt.')
  return receipt
}

/** Count calls to the bundled doorbell asset. */
export async function doorbellCount(page: Page): Promise<number> {
  return (await soundProbeSnapshot(page)).plays.filter(source => source.includes(DOORBELL_SRC)).length
}

/** Wait for the exact number of sound calls. */
export async function expectDoorbellCount(page: Page, count: number): Promise<void> {
  await expect.poll(() => doorbellCount(page)).toBe(count)
}

/** Check quiet behavior after an applied native idle edge or a completed UI action. */
export async function expectDoorbellQuiet(page: Page, count: number, boundary?: SoundReceiptBoundary): Promise<void> {
  if (boundary)
    await waitForIdleSoundReceipt(page, boundary)
  else
    await expect.poll(async () => (await soundProbeSnapshot(page)).settled.map(parseSettledReceipt).some(receipt => receipt.state === AgentActivityState.IDLE)).toBe(true)
  expect(await doorbellCount(page)).toBe(count)
}
