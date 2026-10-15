import { existsSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { codexTest } from '../codex-fixtures'
import { expectNoNativeControl } from '../helpers/nativeControlObservation'
import { nativeTextStep } from '../helpers/nativeScenario'
import { codexEscalatedCommandToolCall } from '../helpers/providerToolCalls'
import { createTestDirectory } from '../helpers/runDirectory'
import { quotePosixShellArgument } from '../helpers/shellArguments'
import { applyPermissionPreset, expectNoControlBanner, messageContents, sendMessage, visibleOnly, waitForAgentIdle, waitForSettingsHydrated } from '../helpers/ui'
import { CODEX_MANUAL_APPROVAL_PREFIX, codexAutoReviewRule, codexManualApproval } from './autoReview'

const REVIEW_RULE = 'codex-auto-review-denies'
const RATIONALE = 'The scripted fixture treats this write as untrusted.'
const APPROVED_NOTICE = 'You approved one retry of an action that auto-review denied.'

codexTest('records the reader\'s approval of an action that Codex auto-review denied', async ({ native }) => {
  const { page, modelScript } = native
  await modelScript.rule(codexAutoReviewRule(REVIEW_RULE, {
    outcome: 'deny',
    risk_level: 'high',
    user_authorization: 'low',
    rationale: RATIONALE,
  }))
  await waitForSettingsHydrated(page, 'permissionMode')
  await applyPermissionPreset(page, 'smart')

  const file = join(createTestDirectory('codex-auto-review-denied-'), 'result.txt')
  await expectNoNativeControl(native, {
    testId: 'control-banner',
    relatedProof: async () => {
      const start = await modelScript.queue(
        { toolCalls: [codexEscalatedCommandToolCall('auto-review-denied', `printf %s denied-42 > ${quotePosixShellArgument(file)}`)] },
        nativeTextStep(native, 'The reviewer refused the command.'),
      )
      await sendMessage(page, modelScript.prompt('Run the scripted command outside this workspace.'))
      const status = await modelScript.waitForSteps(start + 2)
      await waitForAgentIdle(page)
      expect(status.ruleMatches[REVIEW_RULE]).toBe(1)
    },
  })

  // The reviewer decided in place of the reader: no banner asked, and the command never ran.
  expect(existsSync(file)).toBe(false)
  await expectNoControlBanner(page)
  const review = messageContents(page).getByTestId('auto-review').filter({ hasText: file })
  await expect(review).toHaveAttribute('data-outcome', 'denied')
  await expect(review).toContainText('Auto-review denied (high risk)')
  await expect(review).toContainText(RATIONALE)

  await review.getByTestId('auto-review-approve').click()
  await expect(review.getByTestId('auto-review-approved')).toHaveText('Approved for one retry')
  await expect(visibleOnly(page.getByText(APPROVED_NOTICE))).toHaveCount(1)

  // Codex starts no turn for the approval. It states the approval to the model in the next request of the thread.
  const next = await modelScript.queue(nativeTextStep(native, 'The approval reached the model.'))
  await sendMessage(page, modelScript.prompt('Continue after the approval.'))
  await modelScript.waitForSteps(next + 1)
  await waitForAgentIdle(page)
  const approval = codexManualApproval(await modelScript.requestAt(next))
  expect(approval, 'the next model request states the reader\'s approval').toBeDefined()
  expect(approval).toContain(CODEX_MANUAL_APPROVAL_PREFIX)
  expect(approval).toContain('"outcome": "allowed"')
  expect(approval).toContain(file)
  // The approval allows a retry. It does not run the action.
  expect(existsSync(file)).toBe(false)

  // The Worker records the approval on the row of the review, so the row states it after a reload and offers no
  // second approval.
  await page.reload()
  await waitForSettingsHydrated(page, 'permissionMode')
  const reloadedReview = messageContents(page).getByTestId('auto-review').filter({ hasText: file })
  await expect(reloadedReview.getByTestId('auto-review-approved')).toHaveText('Approved for one retry')
  await expect(reloadedReview.getByTestId('auto-review-approve')).toHaveCount(0)
  await expect(visibleOnly(page.getByText(APPROVED_NOTICE))).toHaveCount(1)
  expect((await modelScript.status()).ruleMatches[REVIEW_RULE]).toBe(1)
})
