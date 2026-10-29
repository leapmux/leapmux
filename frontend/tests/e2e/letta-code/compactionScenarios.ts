import type { NativeCompactionHold, NativeCompactionOptions, NativeCompactionResult } from '../helpers/manualCompaction'
import type { NativeScenarioContext } from '../helpers/nativeScenario'
import { escapeRegExp } from '../../../src/lib/regexp'
import { exerciseNativeCompaction } from '../helpers/manualCompaction'

/** Both native local summarizers carry this instruction, independent of the compaction mode. */
const LETTA_SUMMARIZER_PATTERN = escapeRegExp('1.**High level goals**: What is the high level goal and ongoing task?')

export const LETTA_COMPACTION_OPTIONS: NativeCompactionOptions = {
  summary: {
    route: 'rule',
    when: { body: LETTA_SUMMARIZER_PATTERN },
  },
}

/** Run the native command and verify that its summary replaces older model context. */
export function exerciseLettaCompaction(context: NativeScenarioContext, hold: NativeCompactionHold = {}): Promise<NativeCompactionResult> {
  return exerciseNativeCompaction(context, {
    ...LETTA_COMPACTION_OPTIONS,
    ...hold,
  })
}
