import type { ManagedNativeScenarioContext } from './nativeScenario'
import { randomUUID } from 'node:crypto'
import { expect } from '@playwright/test'
import { finishCleanup, withCleanup } from './cleanup'
import { currentNativeAgent } from './nativeScenario'
import { observeSettledReceipts, waitForIdleSoundReceipt } from './turnEndSound'

interface NativeControlObservation {
  seen: boolean
  observer: MutationObserver
}

declare global {
  interface Window {
    __nativeControlObservations?: Record<string, NativeControlObservation>
  }
}

/** Install a browser observation before the real native control path starts. */
export function installNativeControlObservation(options: { id: string, testId: string }): void {
  if (!/^[a-z0-9-]+$/.test(options.testId))
    throw new Error('The native control observation requires one test ID.')
  const observations = window.__nativeControlObservations ??= {}
  observations[options.id]?.observer.disconnect()
  const inspect = () => {
    const state = observations[options.id]
    if (!state)
      return
    const controls = [...document.querySelectorAll<HTMLElement>(`[data-testid="${options.testId}"]`)]
    if (controls.some(control => control.getClientRects().length > 0 && getComputedStyle(control).visibility !== 'hidden'))
      state.seen = true
  }
  const observer = new MutationObserver(inspect)
  observations[options.id] = { seen: false, observer }
  observer.observe(document.body, { subtree: true, childList: true, attributes: true })
  inspect()
}

export function readNativeControlObservation(id: string): boolean {
  const state = window.__nativeControlObservations?.[id]
  if (!state)
    throw new Error('The native control observation disappeared during the proof.')
  return state.seen
}

export function disposeNativeControlObservation(id: string): void {
  const observations = window.__nativeControlObservations
  observations?.[id]?.observer.disconnect()
  if (observations)
    delete observations[id]
  if (observations && Object.keys(observations).length === 0)
    delete window.__nativeControlObservations
}

/** Observe a real native operation until the Worker applies its new idle edge. */
export async function expectNoNativeControl(
  context: ManagedNativeScenarioContext,
  options: { testId: string, additionalTestIds?: readonly string[], relatedControl: () => Promise<void> },
): Promise<void> {
  await assertNoNativeControl(context, options)
}

/** Observe the native startup route before the new agent resolves its first control. */
export async function expectNoNativeStartupControl(
  context: ManagedNativeScenarioContext,
  options: { testId: string, additionalTestIds?: readonly string[], start: () => Promise<void>, relatedControl: () => Promise<void>, nativeCompletion?: () => Promise<void> },
): Promise<void> {
  await assertNoNativeControl(context, options)
}

async function assertNoNativeControl(
  context: ManagedNativeScenarioContext,
  options: { testId: string, additionalTestIds?: readonly string[], start?: () => Promise<void>, relatedControl: () => Promise<void>, nativeCompletion?: () => Promise<void> },
): Promise<void> {
  const observations = [options.testId, ...(options.additionalTestIds ?? [])].map(testId => ({ id: randomUUID(), testId }))
  await withCleanup(async () => {
    for (const observation of observations)
      await context.page.evaluate(installNativeControlObservation, observation)
    await options.start?.()
    const boundary = options.nativeCompletion
      ? null
      : {
          agent: await currentNativeAgent(context),
          after: await observeSettledReceipts(context.page),
        }
    await options.relatedControl()
    if (options.nativeCompletion)
      await options.nativeCompletion()
    else if (boundary)
      await waitForIdleSoundReceipt(context.page, { agentId: boundary.agent.id, after: boundary.after })
    for (const observation of observations)
      expect(await context.page.evaluate(readNativeControlObservation, observation.id)).toBe(false)
  }, () => finishCleanup(observations.map(observation => context.page.evaluate(disposeNativeControlObservation, observation.id))))
}
