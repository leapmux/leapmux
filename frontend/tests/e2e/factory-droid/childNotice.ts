import type { MockModelRule, MockModelStep } from '../helpers/mockModelScript'

/** Match one current native completion for the exact child description. */
export function droidChildNoticeRule(
  description: string,
  respond: MockModelStep,
  name = 'droid-native-child-notice',
): MockModelRule {
  if (!description.trim())
    throw new Error('The native child notice requires its current description.')
  const literalDescription = description.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
  return {
    name,
    when: {
      lastMessage: {
        role: 'system',
        text: ['^Background task completed\\.\\r?\\n', `(?:^|\\n)description: ${literalDescription}\\r?(?:\\n|$)`],
      },
    },
    respond,
    once: true,
  }
}
