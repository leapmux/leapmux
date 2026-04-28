import type { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import { extractOpenCodeQuestions, OpenCodeControlActions, OpenCodeControlContent, sendOpenCodeQuestionResponse } from '../controls/OpenCodeControlRequest'
import { registerACPProvider } from './acp/registerACPProvider'

interface OpenCodeProtocolOptions {
  provider: AgentProvider
  /** Default model identifier (typically read from LEAPMUX_*_DEFAULT_MODEL). */
  defaultModel: string
  /** Default primary-agent option, e.g. `'build'` for OpenCode, `'code'` for Kilo. */
  defaultPrimaryAgent: string
}

const PRIMARY_AGENT_KEY = 'primaryAgent'
const PLAN_PRIMARY_AGENT = 'plan'

/**
 * Register a provider that speaks the OpenCode question/control protocol.
 * OpenCode and Kilo run different daemons but share the same wire format —
 * the only deltas are the provider enum, the default model env-var, and the
 * default primary-agent label.
 */
export function registerOpenCodeProtocolProvider(opts: OpenCodeProtocolOptions): void {
  registerACPProvider({
    provider: opts.provider,
    settingsConfig: {
      kind: 'optionGroup',
      defaultModel: opts.defaultModel,
      optionGroupKey: PRIMARY_AGENT_KEY,
      defaultValue: opts.defaultPrimaryAgent,
      fallbackLabel: 'Primary Agent',
      testIdPrefix: 'primary-agent',
    },
    ControlContent: OpenCodeControlContent,
    ControlActions: OpenCodeControlActions,
    planValue: PLAN_PRIMARY_AGENT,
    questionHandling: {
      isAskUserQuestion: payload => payload?.type === 'question.asked',
      extractAskUserQuestions: extractOpenCodeQuestions,
      sendAskUserQuestionResponse: sendOpenCodeQuestionResponse,
    },
  })
}
