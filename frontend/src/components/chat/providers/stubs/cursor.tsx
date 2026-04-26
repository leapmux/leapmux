import type { ACPSettingsPanelConfig } from '../acp/settings'
import type { PermissionMode } from '~/utils/controlResponse'
import { createMemo, Show } from 'solid-js'
import { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import { ACPControlActions, ACPControlContent } from '../../controls/ACPControlRequest'
import { CursorControlActions, CursorControlContent, getCursorQuestions, isCursorAskQuestionPayload, isCursorControlPayload, sendCursorQuestionResponse } from '../../controls/CursorControlRequest'
import { registerACPProvider } from '../acp/registerACPProvider'

const DEFAULT_CURSOR_MODEL = import.meta.env.LEAPMUX_CURSOR_DEFAULT_MODEL || 'auto'

const settingsConfig: ACPSettingsPanelConfig = {
  kind: 'permissionMode',
  defaultModel: DEFAULT_CURSOR_MODEL,
  defaultMode: 'agent' as PermissionMode,
  fallbackLabel: 'Mode',
  testIdPrefix: 'permission-mode',
}

registerACPProvider({
  provider: AgentProvider.CURSOR,
  settingsConfig,
  // Cursor's ask-question payload is shaped differently from generic ACP, so
  // dispatch on payload type and fall through to the shared ACP UI when not.
  ControlContent: (props) => {
    const isCursor = createMemo(() => isCursorControlPayload(props.request.payload))
    return (
      <Show when={isCursor()} fallback={<ACPControlContent {...props} />}>
        <CursorControlContent {...props} />
      </Show>
    )
  },
  ControlActions: (props) => {
    const isCursor = createMemo(() => isCursorControlPayload(props.request.payload))
    return (
      <Show when={isCursor()} fallback={<ACPControlActions {...props} />}>
        <CursorControlActions {...props} />
      </Show>
    )
  },
  planValue: 'plan',
  extraHiddenSessionUpdates: new Set(['config_option_update']),
  questionHandling: {
    isAskUserQuestion: payload => !!payload && isCursorAskQuestionPayload(payload),
    extractAskUserQuestions: payload => getCursorQuestions(payload),
    sendAskUserQuestionResponse: sendCursorQuestionResponse,
  },
})
