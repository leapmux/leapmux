import type { Component } from 'solid-js'
import type { ActionsProps, AskQuestionState, ContentProps, Question } from '../../controls/types'

import type { CodexDecision } from './controlResponse'
import { createMemo, createSignal, Match, Show, Switch } from 'solid-js'
import { isObject, pickObject } from '~/lib/jsonPick'
import { buildAllowResponse, buildDenyResponse, getToolInput, getToolName } from '~/utils/controlResponse'
import * as styles from '../../ControlRequestBanner.css'
import { CollapsibleText } from '../../controls/CollapsibleText'
import { ControlDecisionFooter } from '../../controls/ControlDecisionFooter'
import { createPlanApprovalState, planApprovalSwitches } from '../../controls/planApproval'
import { sendJsonRpcResult, sendResponse } from '../../controls/types'
import { codexDecisionKey, codexDecisionLabel, parseCodexDecision } from './controlResponse'

/** Extract Codex approval params from the control request payload. */
function getCodexParams(payload: Record<string, unknown>): Record<string, unknown> | undefined {
  return pickObject(payload, 'params', undefined)
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
  return sendJsonRpcResult(agentId, onRespond, requestId, { decision })
}

export function markCodexPlanPromptResponse(response: Record<string, unknown>): Record<string, unknown> {
  return { ...response, codexPlanModePrompt: true }
}

function sendCodexPlanPromptResponse(
  agentId: string,
  onRespond: (agentId: string, content: Uint8Array) => Promise<void>,
  response: Record<string, unknown>,
): Promise<void> {
  return sendResponse(agentId, onRespond, markCodexPlanPromptResponse(response))
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
  return sendJsonRpcResult(agentId, onRespond, requestId, { answers })
}

export function sendCodexUserInputRejectResponse(
  agentId: string,
  onRespond: (agentId: string, content: Uint8Array) => Promise<void>,
  requestId: string,
): Promise<void> {
  return sendJsonRpcResult(agentId, onRespond, requestId, { answers: {} })
}

export function sendCodexPermissionsResponse(
  agentId: string,
  onRespond: (agentId: string, content: Uint8Array) => Promise<void>,
  requestId: string,
  permissions: Record<string, unknown>,
  scope: 'turn' | 'session',
): Promise<void> {
  return sendJsonRpcResult(agentId, onRespond, requestId, { permissions, scope })
}

function isNegativeDecision(decision: CodexDecision): boolean {
  if (decision === 'decline' || decision === 'cancel')
    return true
  return typeof decision === 'object'
    && 'applyNetworkPolicyAmendment' in decision
    && decision.applyNetworkPolicyAmendment.network_policy_amendment.action === 'deny'
}

function remembersAllow(decision: CodexDecision): boolean {
  if (decision === 'acceptForSession')
    return true
  if (typeof decision !== 'object')
    return false
  if ('acceptWithExecpolicyAmendment' in decision)
    return true
  return decision.applyNetworkPolicyAmendment.network_policy_amendment.action === 'allow'
}

export interface ResolvedCodexDecisions {
  negative: CodexDecision
  positive: CodexDecision
  remembered?: CodexDecision
  additional: CodexDecision[]
}

export function resolveCodexDecisions(raw: unknown): ResolvedCodexDecisions {
  const parsed = Array.isArray(raw)
    ? raw.map(parseCodexDecision).filter((decision): decision is CodexDecision => decision !== null)
    : []
  const decisions: CodexDecision[] = parsed.length > 0 ? parsed : ['accept', 'cancel']
  const negative = decisions.find(isNegativeDecision) ?? 'cancel'
  const positive = decisions.find(decision => decision === 'accept')
    ?? decisions.find(decision => !isNegativeDecision(decision))
    ?? 'accept'
  const remembered = positive === 'accept'
    ? decisions.find(decision => typeof decision === 'object' && remembersAllow(decision))
    ?? decisions.find(decision => decision === 'acceptForSession')
    : undefined
  const additional = decisions.filter(decision => decision !== negative && decision !== positive && decision !== remembered)
  return { negative, positive, remembered, additional }
}

export function codexRequestedPermissions(payload: Record<string, unknown>): Record<string, unknown> {
  const permissions = pickObject(getCodexParams(payload), 'permissions', undefined)
  if (!permissions)
    return {}
  const granted: Record<string, unknown> = {}
  if (isObject(permissions.network))
    granted.network = permissions.network
  if (isObject(permissions.fileSystem))
    granted.fileSystem = permissions.fileSystem
  return granted
}

/** Codex-specific control request content. */
export const CodexControlContent: Component<ContentProps> = (props) => {
  const toolName = () => getToolName(props.request.payload)
  const params = () => getCodexParams(props.request.payload)
  const method = () => props.request.payload.method as string | undefined
  const reason = () => params()?.reason as string | undefined
  const command = () => params()?.command as string | undefined
  const cwd = () => params()?.cwd as string | undefined
  const permissions = () => codexRequestedPermissions(props.request.payload)
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
          <Show when={method() === 'item/permissions/requestApproval'}>
            <CollapsibleText text={JSON.stringify(permissions(), null, 2)} maxLines={6} class={styles.bannerCodeBlock} />
          </Show>
        </>
      )}
    >
      <Match when={toolName() === 'CodexPlanModePrompt'}>
        <div class={styles.controlBannerTitle}>Implement the proposed plan?</div>
      </Match>
    </Switch>
  )
}

const CodexPermissionsActions: Component<ActionsProps> = (props) => {
  const [remember, setRemember] = createSignal(false)
  const [bypass, setBypass] = createSignal(false)
  const handleAllow = async () => {
    await sendCodexPermissionsResponse(
      props.request.agentId,
      props.onRespond,
      props.request.requestId,
      codexRequestedPermissions(props.request.payload),
      remember() ? 'session' : 'turn',
    )
    if (bypass() && props.bypass)
      await props.bypass.apply(props.bypass.settings)
  }
  return (
    <ControlDecisionFooter
      hasEditorContent={props.hasEditorContent}
      onSendFeedback={props.onTriggerSend}
      negativeAction={{
        label: 'Deny',
        testId: 'control-deny-btn',
        onSelect: () => sendCodexPermissionsResponse(props.request.agentId, props.onRespond, props.request.requestId, {}, 'turn'),
      }}
      positiveAction={{ label: 'Allow', testId: 'control-allow-btn', onSelect: handleAllow }}
      switches={() => [
        { id: 'control-remember-checkbox', label: 'Remember', checked: remember(), onChange: setRemember },
        ...(props.bypass
          ? [{ id: 'control-bypass-permissions-checkbox', label: 'Bypass Permissions', checked: bypass(), onChange: setBypass }]
          : []),
      ]}
    />
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
      switches={() => planApprovalSwitches(planApproval, props.bypass)}
    />
  )
}

/** Codex-specific control request action buttons. */
export const CodexControlActions: Component<ActionsProps> = (props) => {
  const toolName = () => getToolName(props.request.payload)
  const method = () => props.request.payload.method as string | undefined
  const params = () => getCodexParams(props.request.payload)
  const decisions = createMemo(() => resolveCodexDecisions(params()?.availableDecisions))
  const [remember, setRemember] = createSignal(false)
  const [bypass, setBypass] = createSignal(false)

  const handleDecision = (decision: CodexDecision) => sendCodexDecision(
    props.request.agentId,
    props.onRespond,
    props.request.requestId,
    decision,
  )

  const handleAllow = async () => {
    await handleDecision(remember() ? (decisions().remembered ?? decisions().positive) : decisions().positive)
    if (bypass() && props.bypass)
      await props.bypass.apply(props.bypass.settings)
  }

  return (
    <Switch
      fallback={(
        <ControlDecisionFooter
          hasEditorContent={props.hasEditorContent}
          onSendFeedback={props.onTriggerSend}
          negativeAction={{ label: codexDecisionLabel(decisions().negative), testId: 'control-deny-btn', onSelect: () => handleDecision(decisions().negative) }}
          positiveAction={{ label: codexDecisionLabel(decisions().positive), testId: 'control-allow-btn', onSelect: handleAllow }}
          switches={() => [
            ...(decisions().remembered
              ? [{
                  id: 'control-remember-checkbox',
                  label: 'Remember',
                  checked: remember(),
                  onChange: setRemember,
                }]
              : []),
            ...(props.bypass
              ? [{
                  id: 'control-bypass-permissions-checkbox',
                  label: 'Bypass Permissions',
                  checked: bypass(),
                  onChange: setBypass,
                }]
              : []),
          ]}
          additionalActions={() => decisions().additional.map(decision => ({
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
      <Match when={method() === 'item/permissions/requestApproval'}>
        <CodexPermissionsActions {...props} />
      </Match>
    </Switch>
  )
}
