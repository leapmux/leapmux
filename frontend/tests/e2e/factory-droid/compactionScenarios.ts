import type { NativeCompactionOptions } from '../helpers/manualCompaction'
import type { NativeScenarioContext } from '../helpers/nativeScenario'
import { expect } from '../droid-fixtures'
import { expectCompactionNotice } from '../helpers/compaction'
import { exerciseNativeCompaction, OLDER_CONTEXT_MARKER } from '../helpers/manualCompaction'
import { messageBubbles } from '../helpers/ui'

/**
 * The native compaction of Factory Droid.
 * `/compact` is Droid's own slash command. Its summarizer request falls through to the fallback, and the request must
 * carry the older context that the summary replaces.
 */
export const DROID_COMPACTION: NativeCompactionOptions = {
  summary: { route: 'fallback', requestMarker: OLDER_CONTEXT_MARKER },
}

/** Require that the transcript draws no raw Droid frame of the compaction: no settings update and no compaction event. */
async function expectNoRawCompactionFrames(context: NativeScenarioContext): Promise<void> {
  await expect(messageBubbles(context.page).filter({ hasText: 'settings_updated' })).toHaveCount(0)
  await expect(messageBubbles(context.page).filter({ hasText: '"type":"session_compacted"' })).toHaveCount(0)
}

/** Run the native compaction, then require its notice and no raw compaction frame, before and after a reload. */
export async function exerciseCompletedManualCompaction(context: NativeScenarioContext): Promise<void> {
  await exerciseNativeCompaction(context, DROID_COMPACTION)
  await expectCompactionNotice(context.page)
  await expectNoRawCompactionFrames(context)
  await context.page.reload()
  await expectCompactionNotice(context.page)
  await expectNoRawCompactionFrames(context)
}
