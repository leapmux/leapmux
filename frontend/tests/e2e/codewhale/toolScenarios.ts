import type { Page } from '@playwright/test'
import { chooseSettingsOption, waitForSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'
import { expectCodewhalePosture } from './scenarios'

/** Select native Full Access and wait for its confirmed posture. */
export async function runWithoutApprovals(page: Page): Promise<void> {
  await waitForSettingsHydrated(page)
  await chooseSettingsOption(page, 'permissionMode-full_access')
  await waitForSettingsIdle(page)
  await expectCodewhalePosture(page, 'full_access')
}
