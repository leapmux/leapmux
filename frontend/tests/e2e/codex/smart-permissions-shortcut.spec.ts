import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { codexTest } from '../codex-fixtures'
import { expectNoNativeControl } from '../helpers/nativeControlObservation'
import { nativeTextStep } from '../helpers/nativeScenario'
import { codexEscalatedCommandToolCall } from '../helpers/providerToolCalls'
import { createTestDirectory } from '../helpers/runDirectory'
import { quotePosixShellArgument } from '../helpers/shellArguments'
import { applyPermissionPreset, expectNoControlBanner, expectPermissionShortcuts, expectSettingsOptionChosen, messageContents, sendMessage, waitForAgentIdle, waitForSettingsHydrated } from '../helpers/ui'
import { codexAutoReviewRule } from './autoReview'

const REVIEW_RULE = 'codex-auto-review-allows'

codexTest('approves an escalated command through Codex auto-review and restores the preset after reload', async ({ native }) => {
  const { page, modelScript } = native
  await modelScript.rule(codexAutoReviewRule(REVIEW_RULE, {
    outcome: 'allow',
    risk_level: 'low',
    user_authorization: 'high',
    rationale: 'The user asked for this reversible fixture write.',
  }))
  await waitForSettingsHydrated(page, 'permissionMode')
  await expectSettingsOptionChosen(page, 'permissionMode-on-request')
  await applyPermissionPreset(page, 'smart')

  for (const reload of [false, true]) {
    if (reload) {
      await page.reload()
      await waitForSettingsHydrated(page, 'permissionMode')
    }
    // Upstream Codex calls this preset Approve for me: the auto-review reviewer on the workspace profile.
    await expectSettingsOptionChosen(page, 'permissionMode-auto-review')
    await expectSettingsOptionChosen(page, 'permission_profile-:workspace')
    await expectPermissionShortcuts(page, { smart: 'disabled', bypass: 'offered' })

    // The workspace profile refuses a write outside the workspace, so only an approved escalation writes the marker.
    const file = join(createTestDirectory('codex-auto-review-allowed-'), 'result.txt')
    const marker = reload ? 'auto-review-restored-42' : 'auto-review-applied-42'
    const reviews = (await modelScript.status()).ruleMatches[REVIEW_RULE] ?? 0
    await expectNoNativeControl(native, {
      testId: 'control-banner',
      relatedProof: async () => {
        // One agent session runs both passes, so each pass gives its tool call its own ID.
        const start = await modelScript.queue(
          { toolCalls: [codexEscalatedCommandToolCall(`auto-review-allowed-${Number(reload)}`, `printf %s ${quotePosixShellArgument(marker)} > ${quotePosixShellArgument(file)}`)] },
          nativeTextStep(native, 'The reviewer approved the command.'),
        )
        await sendMessage(page, modelScript.prompt('Run the scripted command outside this workspace.'))
        const status = await modelScript.waitForSteps(start + 2)
        await waitForAgentIdle(page)
        // Codex asks its reviewer once for each escalation and never reuses an earlier verdict.
        expect(status.ruleMatches[REVIEW_RULE]).toBe(reviews + 1)
      },
    })

    expect(readFileSync(file, 'utf8')).toBe(marker)
    await expectNoControlBanner(page)
    const review = messageContents(page).getByTestId('auto-review').filter({ hasText: file })
    await expect(review).toHaveAttribute('data-outcome', 'approved')
    await expect(review).toContainText('Auto-review approved')
    // An approval offers no action to the reader.
    await expect(review.getByTestId('auto-review-approve')).toHaveCount(0)
  }
})
