import { join } from 'node:path'
import { expect } from '@playwright/test'
import { cursorTest } from '../cursor-fixtures'
import { exerciseNativePermissionReason, exerciseRememberedAllow, expectDeclinedToolRowAcrossReload } from '../helpers/nativePermission'
import { cursorWebFetchPermissionToolCall } from '../helpers/providerToolCalls'
import { uniqueMarker } from '../helpers/shellArguments'
import { answerControl, assistantBubbles, expectNoControlBanner, savedControlAnswer, sendMessage, waitForAgentIdle, waitForControlBanner } from '../helpers/ui'

// The Cursor CLI answers its own Run stream with the result of the web fetch, and the mock Cursor service ends the
// turn with that result. So each turn queues the tool call alone, and the turn needs no second model answer.

/** The text that the mock Run service writes for a web fetch that the CLI approved. */
const CURSOR_WEB_FETCH_APPROVAL = 'Cursor web fetch approved'

cursorTest('forwards a native web-fetch permission decision', async ({ native }) => {
  const { page, modelScript } = native
  const start = await modelScript.queue({ toolCalls: [cursorWebFetchPermissionToolCall('cursor-fetch', 'https://example.invalid/cursor-probe')] })
  await sendMessage(page, modelScript.prompt('Ask permission to fetch the scripted URL.'))
  await modelScript.waitForSteps(start + 1)
  const banner = await waitForControlBanner(page)
  await expect(banner).toContainText('https://example.invalid/cursor-probe')
  await answerControl(page, 'allow')

  await waitForAgentIdle(page)
  await expect(assistantBubbles(page).filter({ hasText: CURSOR_WEB_FETCH_APPROVAL }).first()).toBeVisible()
  // The saved row reads the native option that the decision selected.
  await expect(savedControlAnswer(page)).toHaveText('Allow once')
  await page.reload()
  await expect(assistantBubbles(page).filter({ hasText: CURSOR_WEB_FETCH_APPROVAL }).first()).toBeVisible()
})

/**
 * The Deny button selects the ACP option reject-once.
 * The Cursor CLI then answers the web-fetch query with its rejected branch and the reason "User rejected".
 * The mock Run service writes this text only for an answer that carries the ID of the pending query.
 * An answer with any other ID ends the stream, and no text arrives.
 */
const CURSOR_WEB_FETCH_REFUSAL = 'Cursor web fetch rejected: User rejected'

cursorTest('forwards a native web-fetch Deny decision', async ({ native }) => {
  const { page, modelScript } = native
  // The CLI skips the question for a domain on its allowlist, and "Allow always" adds one.
  // A separate reserved domain keeps this question independent of any approval of example.invalid.
  const url = 'https://cursor-denied-probe.invalid/native-web-fetch'
  const start = await modelScript.queue({ toolCalls: [cursorWebFetchPermissionToolCall('cursor-denied-fetch', url)] })
  await sendMessage(page, modelScript.prompt('Ask permission to fetch the scripted private URL.'))
  await modelScript.waitForSteps(start + 1)
  const banner = await waitForControlBanner(page)
  await expect(banner).toContainText(url)
  await answerControl(page, 'deny')

  await waitForAgentIdle(page)
  await expectNoControlBanner(page)
  await expect(assistantBubbles(page).filter({ hasText: CURSOR_WEB_FETCH_REFUSAL }).first()).toBeVisible()
  await expect(assistantBubbles(page).filter({ hasText: CURSOR_WEB_FETCH_APPROVAL })).toHaveCount(0)
  await expect(savedControlAnswer(page)).toHaveText('Reject')
  // The native rejected result keeps its reason before and after a reload.
  await expectDeclinedToolRowAcrossReload(native, 'cursor-denied-fetch', 'User rejected')
  await expect(assistantBubbles(page).filter({ hasText: CURSOR_WEB_FETCH_REFUSAL }).first()).toBeVisible()
  await expect(assistantBubbles(page).filter({ hasText: CURSOR_WEB_FETCH_APPROVAL })).toHaveCount(0)
})

// The ACP reply selects an option, and an option carries no text. The reason follows as the reader's next message,
// which opens a Run of its own after the refused one ends.
cursorTest('sends the reader\'s typed refusal reason as the next message', async ({ native }) => {
  const { page } = native
  // A domain of its own keeps this question independent of any allowlist entry.
  const url = `https://cursor-reason-${uniqueMarker()}.invalid/native-web-fetch`
  await exerciseNativePermissionReason(native, {
    toolCall: cursorWebFetchPermissionToolCall('cursor-reason-fetch', url),
    route: 'next-message',
    afterRefusal: 'ends',
    beforeDecision: banner => expect(banner).toContainText(url),
    expectNotRun: () => expect(assistantBubbles(page).filter({ hasText: CURSOR_WEB_FETCH_APPROVAL })).toHaveCount(0),
    viewProof: () => expect(savedControlAnswer(page)).toHaveText('Reject'),
  })
})

// "Allow always" adds the domain to the CLI's allowlist, so a later fetch of the same domain runs with no question.
cursorTest('an always answer covers a later fetch of the same domain', async ({ native }) => {
  const { page } = native
  const home = native.leapmuxServer.agentEnv?.HOME
  if (!home)
    throw new Error('The always scenario requires the isolated HOME of the Cursor CLI.')
  // A domain of its own keeps the kept rule away from every other question of the run.
  const domain = `https://cursor-always-${uniqueMarker()}.invalid`
  const approvals = assistantBubbles(page).filter({ hasText: CURSOR_WEB_FETCH_APPROVAL })
  await exerciseRememberedAllow(native, {
    scope: 'Always',
    answerStep: 'same-step',
    firstCall: cursorWebFetchPermissionToolCall('cursor-always-first', `${domain}/first`),
    secondCall: cursorWebFetchPermissionToolCall('cursor-always-second', `${domain}/second`),
    beforeDecision: banner => expect(banner).toContainText(`${domain}/first`),
    // The Run exchange holds the whole turn, so the page states the result of each fetch.
    firstProof: () => expect(approvals).toHaveCount(1),
    secondProof: () => expect(approvals).toHaveCount(2),
    viewProof: () => expect(savedControlAnswer(page)).toHaveText('Allow always'),
    // The CLI keeps its allowlist in its own configuration under the HOME of the run.
    ruleFiles: [join(home, '.cursor', 'cli-config.json')],
  })
})
