import type { MockModelMatcher } from '../helpers/mockModelScript'
import { escapeRegExp } from '../../../src/lib/regexp'

/** Build a matcher for this script's exact child task. */
export function diracChildRequestMatcher(task: string): MockModelMatcher {
  if (!task.trim())
    throw new Error('The native Dirac child request requires a task.')
  return {
    userHistory: escapeRegExp(task),
  }
}
