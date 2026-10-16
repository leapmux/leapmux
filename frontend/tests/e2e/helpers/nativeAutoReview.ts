import type { ManagedNativeScenarioContext } from './nativeScenario'
import type { NativeToolTurn, NativeToolTurnRequests } from './nativeToolExecution'
import { expect } from '@playwright/test'
import { expectNoNativeControl } from './nativeControlObservation'
import { runNativeToolTurn } from './nativeToolExecution'

/** Require one fresh native reviewer request while a tool turn runs without a manual approval. */
export async function runReviewedNativeToolTurn(
  context: ManagedNativeScenarioContext,
  ruleName: string,
  turn: Omit<NativeToolTurn, 'permissions'>,
): Promise<NativeToolTurnRequests> {
  if (!ruleName.trim())
    throw new Error('A native review proof requires a rule name.')
  const before = (await context.modelScript.status()).ruleMatches[ruleName] ?? 0
  let requests: NativeToolTurnRequests | undefined
  await expectNoNativeControl(context, {
    testId: 'control-banner',
    relatedProof: async () => {
      requests = await runNativeToolTurn(context, { ...turn, permissions: 'none' })
      const after = await context.modelScript.status()
      expect(after.ruleMatches[ruleName], 'the native reviewer decides this operation exactly once').toBe(before + 1)
    },
  })
  if (!requests)
    throw new Error('The native review proof did not run its tool turn.')
  return requests
}
