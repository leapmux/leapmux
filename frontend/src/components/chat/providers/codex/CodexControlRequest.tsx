import type { Component } from 'solid-js'
import type { ActionsProps, AskQuestionState, ContentProps, Question } from '../../controls/types'

import type { CodexDecision } from './controlResponse'
import { createSignal, Match, Show, Switch } from 'solid-js'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { isObject } from '~/lib/jsonPick'
import { buildAllowResponse, buildDenyResponse, getToolInput, getToolName } from '~/utils/controlResponse'
import * as styles from '../../ControlRequestBanner.css'
import { AskUserQuestionActions, AskUserQuestionContent } from '../../controls/AskUserQuestionControl'
import { CollapsibleText } from '../../controls/CollapsibleText'
import { ControlDecisionFooter } from '../../controls/ControlDecisionFooter'
import { createPlanApprovalState, planApprovalSwitches } from '../../controls/planApproval'
import { sendResponse, toRpcId } from '../../controls/types'
import { CODEX_BYPASS_PERMISSION_SETTINGS } from './constants'
import { codexDecisionKey, codexDecisionLabel } from './controlResponse'

/** Extract Codex approval params from the control request payload. */
function getCodexParams(payload: Record<string, unknown>): Record<string, unknown> | undefined {
  return payload.params as Record<string, unknown> | undefined
}

/**
 * Sends a Codex-native approval decision as a JSON-RPC response directly.
 */
export function sendCodexDecision(
  agentId: string,
  onRespond: (agentId: string, content: Uint8Array) => Promise<void>,
  requestId: string,
  decision: CodexDecision,
): Promise<void> {
  return sendResponse(agentId, onRespond, {
    jsonrpc: '2.0',
    id: toRpcId(requestId),
    result: { decision },
  })
}

function sendCodexPlanPromptResponse(
  agentId: string,
  onRespond: (agentId: string, content: Uint8Array) => Promise<void>,
  response: Record<string, unknown>,
): Promise<void> {
  return sendResponse(agentId, onRespond, {
    ...response,
    codexPlanModePrompt: true,
  })
}

const CODEX_OTHER_OPTION_LABEL = 'None of the above'

function hasCodexOtherOption(question: Question): boolean {
  const raw = question as unknown as Record<string, unknown>
  return raw.isOther === true && Array.isArray(question.options) && question.options.length > 0
}

function codexAnswerValues(question: Question, index: number, askState: AskQuestionState): string[] {
  const selected = askState.selections()[index] ?? []
  const customText = askState.customTexts()[index]?.trim()
  const values = [...selected]

  if (customText) {
    if (values.length === 0 && hasCodexOtherOption(question)) {
      // Codex marks its auto-added free-form option explicitly.
      values.push(CODEX_OTHER_OPTION_LABEL)
    }
    // Codex's TUI appends free-form text as a user_note answer entry,
    // even for questions without a selected option.
    values.push(`user_note: ${customText}`)
  }

  return values
}

/**
 * Sends a Codex-native requestUserInput response as a JSON-RPC response directly.
 */
export function sendCodexUserInputResponse(
  agentId: string,
  onRespond: (agentId: string, content: Uint8Array) => Promise<void>,
  requestId: string,
  questions: Question[],
  askState: AskQuestionState,
): Promise<void> {
  const answers: Record<string, { answers: string[] }> = {}
  for (let i = 0; i < questions.length; i++) {
    const values = codexAnswerValues(questions[i], i, askState)
    const key = questions[i].id || questions[i].header || `q${i}`
    answers[key] = { answers: values }
  }
  return sendResponse(agentId, onRespond, {
    jsonrpc: '2.0',
    id: toRpcId(requestId),
    result: { answers },
  })
}

/**
 * Wraps a Codex requestUserInput payload into the synthetic format that
 * AskUserQuestionContent/getToolInput expects (payload.request.input.questions).
 */
function wrapAsAskUserQuestion(payload: Record<string, unknown>): Record<string, unknown> {
  const params = payload.params as Record<string, unknown> | undefined
  return {
    ...payload,
    request: {
      tool_name: 'AskUserQuestion',
      input: { questions: params?.questions ?? [] },
    },
  }
}

/** Find the Codex decision that records an allow choice for future requests. */
function rememberedAllowDecision(decisions: CodexDecision[] | undefined): CodexDecision | undefined {
  return decisions?.find(decision => isObject(decision) && 'acceptWithExecpolicyAmendment' in decision)
    ?? decisions?.find(decision => decision === 'acceptForSession')
}

function isCodexDecision(value: unknown): value is CodexDecision {
  return typeof value === 'string' || (isObject(value) && Object.keys(value).length > 0)
}

/** Return decisions that the fixed Deny/Allow controls do not cover. */
function additionalDecisions(decisions: CodexDecision[] | undefined, rememberDecision: CodexDecision | undefined): CodexDecision[] {
  return (decisions ?? []).filter((decision) => {
    if (decision === 'accept' || decision === 'decline' || decision === rememberDecision)
      return false
    return true
  })
}

/** Codex-specific control request content. */
export const CodexControlContent: Component<ContentProps> = (props) => {
  const toolName = () => getToolName(props.request.payload)
  const params = () => getCodexParams(props.request.payload)
  const method = () => props.request.payload.method as string | undefined
  const reason = () => params()?.reason as string | undefined
  const command = () => params()?.command as string | undefined
  const cwd = () => params()?.cwd as string | undefined
  const title = () => {
    const m = method()
    if (m === 'item/commandExecution/requestApproval')
      return 'Command Execution'
    if (m === 'item/fileChange/requestApproval')
      return 'File Change'
    if (m === 'item/permissions/requestApproval')
      return 'Permission Request'
    return 'Approval Required'
  }

  return (
    <Switch
      fallback={(
        <>
          <div class={styles.controlBannerTitle}>{title()}</div>
          <Show when={reason()}>
            <div class={styles.bannerReason}>{reason()}</div>
          </Show>
          <Show when={command()}>
            <CollapsibleText text={command()!} maxLines={6} class={styles.bannerCodeBlock} />
          </Show>
          <Show when={cwd()}>
            <div class={styles.bannerHint}>
              {'cwd: '}
              {cwd()}
            </div>
          </Show>
        </>
      )}
    >
      <Match when={toolName() === 'CodexPlanModePrompt'}>
        <div class={styles.controlBannerTitle}>Implement the proposed plan?</div>
      </Match>
      <Match when={method() === 'item/tool/requestUserInput'}>
        <AskUserQuestionContent
          request={{ ...props.request, payload: wrapAsAskUserQuestion(props.request.payload) }}
          askState={props.askState}
          optionsDisabled={props.optionsDisabled}
          agentProvider={AgentProvider.CODEX}
        />
      </Match>
    </Switch>
  )
}

/** Codex plan-mode prompt actions with clear-context and bypass switches. */
const CodexPlanModePromptActions: Component<ActionsProps> = (props) => {
  const planApproval = createPlanApprovalState(props)

  const handleApprove = () => sendCodexPlanPromptResponse(
    props.request.agentId,
    props.onRespond,
    buildAllowResponse(props.request.requestId, getToolInput(props.request.payload), {
      permissionMode: planApproval.permissionMode(),
      clearContext: planApproval.clearContext(),
    }),
  )

  return (
    <ControlDecisionFooter
      hasEditorContent={props.hasEditorContent}
      onSendFeedback={props.onTriggerSend}
      negativeAction={{
        label: 'Reject',
        testId: 'control-deny-btn',
        onSelect: () => sendCodexPlanPromptResponse(props.request.agentId, props.onRespond, buildDenyResponse(props.request.requestId, '')),
      }}
      positiveAction={{ label: 'Approve', testId: 'control-allow-btn', onSelect: handleApprove }}
      switches={() => planApprovalSwitches(planApproval, props.bypassPermissionMode)}
    />
  )
}

/** Codex-specific control request action buttons. */
export const CodexControlActions: Component<ActionsProps> = (props) => {
  const toolName = () => getToolName(props.request.payload)
  const method = () => props.request.payload.method as string | undefined
  const params = () => getCodexParams(props.request.payload)
  const availableDecisions = () => {
    const decisions = params()?.availableDecisions
    return Array.isArray(decisions) ? decisions.filter(isCodexDecision) : undefined
  }
  const rememberDecision = () => rememberedAllowDecision(availableDecisions())
  const extraDecisions = () => additionalDecisions(availableDecisions(), rememberDecision())
  const questions = () => (params()?.questions as Question[] | undefined) ?? []
  const [remember, setRemember] = createSignal(false)
  const [bypass, setBypass] = createSignal(false)

  const handleDecision = (decision: CodexDecision) => sendCodexDecision(
    props.request.agentId,
    props.onRespond,
    props.request.requestId,
    decision,
  )

  const handleAllow = async () => {
    await sendCodexDecision(props.request.agentId, props.onRespond, props.request.requestId, remember() ? (rememberDecision() ?? 'accept') : 'accept')
    if (bypass())
      await props.onSettingChange?.({ sets: { ...CODEX_BYPASS_PERMISSION_SETTINGS } })
  }

  /**
   * Intercepting onRespond for requestUserInput: AskUserQuestionActions sends
   * Claude Code-style responses; we intercept and re-encode as Codex JSON-RPC.
   */
  const userInputOnRespond = async (_agentId: string, content: Uint8Array) => {
    const parsed = JSON.parse(new TextDecoder().decode(content))
    // Extract answers from Claude Code format and re-send as Codex JSON-RPC.
    const input = parsed?.response?.response?.updatedInput
    const claudeAnswers = input?.answers as Record<string, string> | undefined
    if (claudeAnswers) {
      await sendCodexUserInputResponse(props.request.agentId, props.onRespond, props.request.requestId, questions(), props.askState)
      return
    }
    // Deny / stop — translate to decline
    if (parsed?.response?.response?.behavior === 'deny') {
      await sendCodexDecision(props.request.agentId, props.onRespond, props.request.requestId, 'decline')
      return
    }
    // Fallback: forward as-is
    await props.onRespond(props.request.agentId, content)
  }

  return (
    <Switch
      fallback={(
        <ControlDecisionFooter
          hasEditorContent={props.hasEditorContent}
          onSendFeedback={props.onTriggerSend}
          negativeAction={{ label: 'Deny', testId: 'control-deny-btn', onSelect: () => handleDecision('decline') }}
          positiveAction={{ label: 'Allow', testId: 'control-allow-btn', onSelect: handleAllow }}
          switches={() => [
            ...(rememberDecision()
              ? [{
                  id: 'control-remember-checkbox',
                  label: 'Remember',
                  checked: remember(),
                  onChange: setRemember,
                }]
              : []),
            ...(props.bypassPermissionMode && props.onSettingChange
              ? [{
                  id: 'control-bypass-permissions-checkbox',
                  label: 'Bypass Permissions',
                  checked: bypass(),
                  onChange: setBypass,
                }]
              : []),
          ]}
          additionalActions={() => extraDecisions().map(decision => ({
            label: codexDecisionLabel(decision),
            testId: `control-decision-${codexDecisionKey(decision)}`,
            onSelect: () => handleDecision(decision),
            outline: decision === 'decline' || decision === 'cancel',
          }))}
        />
      )}
    >
      <Match when={toolName() === 'CodexPlanModePrompt'}>
        <CodexPlanModePromptActions {...props} />
      </Match>
      <Match when={method() === 'item/tool/requestUserInput'}>
        <AskUserQuestionActions
          {...props}
          request={{ ...props.request, payload: wrapAsAskUserQuestion(props.request.payload) }}
          onRespond={userInputOnRespond}
          agentProvider={AgentProvider.CODEX}
        />
      </Match>
    </Switch>
  )
}
