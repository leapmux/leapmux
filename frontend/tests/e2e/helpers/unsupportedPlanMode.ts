import type { ManagedNativeScenarioContext } from './nativeScenario'
import { expect } from '@playwright/test'
import { expectNoNativeControl } from './nativeControlObservation'
import { sendNativeAnswer } from './nativeConversation'
import { currentNativeAgent, nativeModelContextText, nativeOptionGroup, nativeTextStep, nativeToolOutcome } from './nativeScenario'
import { waitForNativeToolSteps } from './nativeToolExecution'
import { bashToolCall } from './providerToolCalls'
import { uniqueMarker } from './shellArguments'
import { closeComposerMenus, messageContents, openPlusMenu, openSettingsMenu, sendMessage, waitForNativeSettingsHydrated } from './ui'

/** The test IDs of the two buttons of a plan review. */
export const PLAN_REVIEW_BUTTON_TEST_IDS = ['plan-approve-btn', 'plan-reject-btn'] as const

/** What {@link expectNoPlanReview} runs around its observation. */
export interface NoPlanReviewOptions {
  /** The real native operation that shows the agent works while no plan review appears. */
  relatedProof: () => Promise<void>
  /**
   * Wait for the restored view after the reload. The default waits until the settings menu offers the live native
   * catalog. A provider that also proves a restored mode chip passes its own wait.
   */
  afterReload?: () => Promise<void>
}

/**
 * Prove that a real native operation raises no plan review, and that the reloaded page shows none.
 *
 * One observation watches both plan review buttons for the whole operation, so a stray Reject button fails it as a
 * stray Approve button does. After the reload, the page must hold neither button, visible or hidden.
 */
export async function expectNoPlanReview(context: ManagedNativeScenarioContext, options: NoPlanReviewOptions): Promise<void> {
  const [testId, ...additionalTestIds] = PLAN_REVIEW_BUTTON_TEST_IDS
  await expectNoNativeControl(context, { testId, additionalTestIds, relatedProof: options.relatedProof })
  await context.page.reload()
  await (options.afterReload ?? (() => waitForNativeSettingsHydrated(context.page)))()
  for (const button of PLAN_REVIEW_BUTTON_TEST_IDS)
    await expect(context.page.getByTestId(button), `the reloaded page holds no ${button}`).toHaveCount(0)
}

/** Prove that the launched protocol carries /plan as text and offers no Plan setting. */
export async function exerciseMissingNativePlanMode(context: ManagedNativeScenarioContext, options: { reload?: boolean } = {}): Promise<void> {
  const marker = uniqueMarker()
  await sendNativeAnswer(context, `Keep NOPLANCONTEXT${marker} for the next command.`, 'The actual native mode is ready.')
  const callId = `no-plan-${marker}`
  const output = `NOPLAN${marker}42`
  const start = await context.modelScript.queue(
    { toolCalls: [bashToolCall(context.provider, callId, `printf 'NOPLAN${marker}%s\\n' "$((40 + 2))"`)] },
    nativeTextStep(context, 'The literal plan command reached a working native tool turn.'),
  )
  await sendMessage(context.page, '/plan')
  await waitForNativeToolSteps(context, start + 2)
  const request = await context.modelScript.requestAt(start)
  expect(nativeModelContextText(request)).toContain('/plan')
  expect(nativeModelContextText(request)).toContain(`NOPLANCONTEXT${marker}`)
  const resultRequest = await context.modelScript.requestAt(start + 1)
  expect((await nativeToolOutcome(context, resultRequest, callId)).text).toContain(output)
  await expect(messageContents(context.page).filter({ hasText: output }).first()).toBeVisible()
  for (const reload of options.reload === false ? [false] : [false, true]) {
    if (reload) {
      await context.page.reload()
      await waitForNativeSettingsHydrated(context.page)
    }
    const agent = await currentNativeAgent(context)
    expect(agent.optionGroups.length).toBeGreaterThan(0)
    const choices = agent.optionGroups.flatMap(group => group.options)
    expect(choices.length).toBeGreaterThan(0)
    expect(choices.some(option => /^plan$/i.test(option.id) || /^plan$/i.test(option.name))).toBe(false)
    const menu = await openPlusMenu(context.page)
    await expect(menu.locator('[data-testid$="-plan"]:visible')).toHaveCount(0)
    // Escape closes only the popover that holds the focus, so close every composer menu.
    await closeComposerMenus(context.page)
  }
}

/**
 * Prove that the permission mode group of the agent offers no `plan` value, in the live catalog and in the settings
 * menu, before and after a reload. A native turn comes first: it proves that the agent works, and the agent states
 * its catalog by then.
 */
export async function expectNoPlanOption(context: ManagedNativeScenarioContext): Promise<void> {
  await sendNativeAnswer(context, 'Complete the native mode catalog probe.', 'The native mode catalog probe completed.')
  for (const reload of [false, true]) {
    if (reload)
      await context.page.reload()
    await waitForNativeSettingsHydrated(context.page)
    const mode = nativeOptionGroup(await currentNativeAgent(context), 'permissionMode')
    if (!mode || mode.options.length === 0)
      throw new Error('The native mode catalog is absent.')
    expect(mode.options.map(option => option.id), 'the native mode catalog offers no plan value').not.toContain('plan')
    const menu = await openSettingsMenu(context.page, 'permissionMode')
    await expect(menu.getByTestId('permissionMode-plan'), 'the mode menu offers no plan value').toHaveCount(0)
    await closeComposerMenus(context.page)
  }
}
