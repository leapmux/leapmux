import type { MockModelRule } from '../helpers/mockModelScript'
import { escapeRegExp } from '../../../src/lib/regexp'
import { STEER_BEFORE_TOOL_ANSWER } from '../helpers/nativeToolSteering'

/** Native steering can background the shell, whose completion can start another model request. */
export const COPILOT_STEER_COMPLETION_RULE: MockModelRule = {
  name: 'the native shell completion after steering',
  when: {
    user: '^<system_notification>\\s*Shell command "Run the scripted command" \\(shellId: [^\\r\\n)]+\\) has completed successfully\\.\\s*</system_notification>$',
    // An earlier completion must consume the ordered answer, which this controlled scenario emits only as assistant text.
    body: escapeRegExp(JSON.stringify(STEER_BEFORE_TOOL_ANSWER)),
  },
  respond: { text: 'The native shell completion was received.' },
  once: true,
}
