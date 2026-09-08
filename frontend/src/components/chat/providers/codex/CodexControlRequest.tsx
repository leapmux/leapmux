import type { Component } from 'solid-js'
import type { ControlAllowChoicePill } from '../../controls/ControlPillGroups'
import type { ActionsProps, ContentProps, ControlAnswerState, Question } from '../../controls/types'
import type { CodexDecision } from './controlResponse'
import type { PillOptions } from '~/components/common/PillGroup'

import { createMemo, Match, Show, Switch } from 'solid-js'
import { isPillOptions, PILL_OPTION_LIMIT } from '~/components/common/PillGroup'
import { isObject, pickObject } from '~/lib/jsonPick'
import { buildAllowResponse, buildDenyResponse, getToolInput, getToolName } from '~/utils/controlResponse'
import * as styles from '../../ControlRequestBanner.css'
import { CollapsibleText } from '../../controls/CollapsibleText'
import { ControlDecisionFooter } from '../../controls/ControlDecisionFooter'
import { buildSessionPermissionPill, createSessionPermissionPresetChoice, respondThenApplyPermissionPreset } from '../../controls/permissionPresets'
import { createPlanApprovalState, planApprovalSwitches } from '../../controls/planApproval'
import { CONTROL_ALLOW_CHOICE_ID, createControlChoice, sendJsonRpcResult, sendResponse } from '../../controls/types'
import { codexDecisionKey, codexDecisionLabel, parseCodexDecision } from './controlResponse'

/** Extract Codex approval params from the control request payload. */
function getCodexParams(payload: Record<string, unknown>): Record<string, unknown> | undefined {
  return pickObject(payload, 'params', undefined)
}

/**
 * Sends a Codex-native approval decision as a JSON-RPC response directly.
 */
export function sendCodexDecision(
  onRespond: (content: Uint8Array) => Promise<void>,
  requestId: string,
  decision: CodexDecision,
): Promise<void> {
  return sendJsonRpcResult(onRespond, requestId, { decision })
}

export function markCodexPlanPromptResponse(response: Record<string, unknown>): Record<string, unknown> {
  return { ...response, codexPlanModePrompt: true }
}

function sendCodexPlanPromptResponse(
  onRespond: (content: Uint8Array) => Promise<void>,
  response: Record<string, unknown>,
): Promise<void> {
  return sendResponse(onRespond, markCodexPlanPromptResponse(response))
}

const CODEX_OTHER_OPTION_LABEL = 'None of the above'

function hasCodexOtherOption(question: Question): boolean {
  const raw = question as unknown as Record<string, unknown>
  return raw.isOther === true && Array.isArray(question.options) && question.options.length > 0
}

function codexAnswerValues(question: Question, index: number, answerState: ControlAnswerState): string[] {
  const selected = answerState.selections()[index] ?? []
  const customText = answerState.customTexts()[index]?.trim()
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
  onRespond: (content: Uint8Array) => Promise<void>,
  requestId: string,
  questions: Question[],
  answerState: ControlAnswerState,
): Promise<void> {
  const answers: Record<string, { answers: string[] }> = {}
  for (let i = 0; i < questions.length; i++) {
    const values = codexAnswerValues(questions[i], i, answerState)
    const key = questions[i].id || questions[i].header || `q${i}`
    answers[key] = { answers: values }
  }
  return sendJsonRpcResult(onRespond, requestId, { answers })
}

export function sendCodexUserInputRejectResponse(
  onRespond: (content: Uint8Array) => Promise<void>,
  requestId: string,
): Promise<void> {
  return sendJsonRpcResult(onRespond, requestId, { answers: {} })
}

export function sendCodexPermissionsResponse(
  onRespond: (content: Uint8Array) => Promise<void>,
  requestId: string,
  permissions: Record<string, unknown>,
  scope: 'turn' | 'session',
): Promise<void> {
  return sendJsonRpcResult(onRespond, requestId, { permissions, scope })
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

interface CodexAllowChoice {
  key: string
  label: string
  decision: CodexDecision
}

function codexAllowChoiceLabel(decision: CodexDecision): string {
  if (decision === 'accept')
    return 'Once'
  if (decision === 'acceptForSession')
    return 'Session'
  if (typeof decision === 'object' && 'acceptWithExecpolicyAmendment' in decision)
    return 'Command rule'
  return 'Host rule'
}

function codexAllowChoicePriority(decision: CodexDecision): number {
  if (decision === 'accept')
    return 0
  if (decision === 'acceptForSession')
    return 1
  if (typeof decision === 'object' && 'acceptWithExecpolicyAmendment' in decision)
    return 2
  return 3
}

/**
 * Builds the choices that qualify the shared Allow button. A group requires
 * Codex's one-turn `accept` decision, so its first and default pill is Once.
 */
function codexAllowChoices(decisions: CodexDecision[]): CodexAllowChoice[] | undefined {
  const candidates = decisions
    .map((decision, sourceIndex) => ({ decision, sourceIndex }))
    .filter(candidate => !isNegativeDecision(candidate.decision))
    .sort((a, b) => codexAllowChoicePriority(a.decision) - codexAllowChoicePriority(b.decision))

  if (candidates.length < 2 || candidates[0]?.decision !== 'accept')
    return undefined

  return candidates.slice(0, PILL_OPTION_LIMIT).map(({ decision, sourceIndex }) => ({
    key: `codex-allow-${sourceIndex}`,
    label: codexAllowChoiceLabel(decision),
    decision,
  }))
}

export interface ResolvedCodexDecisions {
  negative: CodexDecision
  positive: CodexDecision
  /** Supports saved in-flight requests that used the former Remember switch. */
  remembered?: CodexDecision
  allowChoices?: CodexAllowChoice[]
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
  const allowChoices = codexAllowChoices(decisions)
  const consumed = new Set<CodexDecision>(allowChoices?.map(choice => choice.decision) ?? [positive])
  consumed.add(negative)
  const additional = decisions.filter(decision => !consumed.has(decision))
  return { negative, positive, remembered, allowChoices, additional }
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
  const allowChoice = createControlChoice(() => props.answerState, CONTROL_ALLOW_CHOICE_ID)
  const permissionChoice = createSessionPermissionPresetChoice(props)
  const selectedScope = (): 'turn' | 'session' => {
    const choice = allowChoice.choice()
    if (choice === 'turn' || choice === 'session')
      return choice
    // Preserve a selection that an in-flight request saved before the switch
    // changed to pills. A new request has neither value and defaults to Once.
    return props.answerState.switches()['control-remember-checkbox'] ? 'session' : 'turn'
  }
  const scopeOptions = [
    { key: 'turn', label: 'Once' },
    { key: 'session', label: 'Session' },
  ] as const satisfies PillOptions<string>
  const handleAllow = () => respondThenApplyPermissionPreset(
    sendCodexPermissionsResponse(
      props.onRespond,
      props.request.requestId,
      codexRequestedPermissions(props.request.payload),
      selectedScope(),
    ),
    props.presets,
    permissionChoice.choice(),
  )
  return (
    <ControlDecisionFooter
      hasEditorContent={props.hasEditorContent}
      onSendFeedback={props.onTriggerSend}
      negativeAction={{
        label: 'Deny',
        testId: 'control-deny-btn',
        onSelect: () => sendCodexPermissionsResponse(props.onRespond, props.request.requestId, {}, 'turn'),
      }}
      positiveAction={{ label: 'Allow', testId: 'control-allow-btn', onSelect: handleAllow }}
      allowChoicePill={() => ({
        label: 'Allow as',
        options: scopeOptions,
        selected: selectedScope(),
        onSelect: allowChoice.setChoice,
      })}
      permissionPill={() => buildSessionPermissionPill(props.presets, permissionChoice)}
    />
  )
}

/** Codex plan-mode prompt actions with clear-context and permission pills. */
const CodexPlanModePromptActions: Component<ActionsProps> = (props) => {
  const planApproval = createPlanApprovalState(props)

  const handleApprove = () => sendCodexPlanPromptResponse(
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
        onSelect: () => sendCodexPlanPromptResponse(props.onRespond, buildDenyResponse(props.request.requestId, '')),
      }}
      positiveAction={{ label: 'Approve', testId: 'control-allow-btn', onSelect: handleApprove }}
      switches={() => planApprovalSwitches(planApproval)}
      permissionPill={planApproval.permissionPill}
    />
  )
}

/** Codex-specific control request action buttons. */
export const CodexControlActions: Component<ActionsProps> = (props) => {
  const toolName = () => getToolName(props.request.payload)
  const method = () => props.request.payload.method as string | undefined
  const params = () => getCodexParams(props.request.payload)
  const decisions = createMemo(() => resolveCodexDecisions(params()?.availableDecisions))
  const allowChoice = createControlChoice(() => props.answerState, CONTROL_ALLOW_CHOICE_ID)
  const permissionChoice = createSessionPermissionPresetChoice(props)

  const selectedAllowChoice = () => {
    const choices = decisions().allowChoices
    if (!choices)
      return undefined
    const selected = choices.find(choice => choice.key === allowChoice.choice())
    if (selected)
      return selected
    if (props.answerState.switches()['control-remember-checkbox']) {
      const migrated = choices.find(choice => choice.decision === decisions().remembered)
      if (migrated)
        return migrated
    }
    return choices[0]
  }

  const allowChoicePill = createMemo<ControlAllowChoicePill | undefined>(() => {
    const choices = decisions().allowChoices
    if (!choices)
      return undefined
    const options = choices.map(choice => ({ key: choice.key, label: choice.label }))
    if (!isPillOptions(options))
      return undefined
    return {
      label: 'Allow as',
      options,
      selected: selectedAllowChoice()?.key ?? options[0].key,
      onSelect: allowChoice.setChoice,
    }
  })

  const handleDecision = (decision: CodexDecision) => sendCodexDecision(
    props.onRespond,
    props.request.requestId,
    decision,
  )

  const handleAllow = () => respondThenApplyPermissionPreset(
    handleDecision(selectedAllowChoice()?.decision ?? decisions().positive),
    props.presets,
    permissionChoice.choice(),
  )

  return (
    <Switch
      fallback={(
        <ControlDecisionFooter
          hasEditorContent={props.hasEditorContent}
          onSendFeedback={props.onTriggerSend}
          negativeAction={{ label: codexDecisionLabel(decisions().negative), testId: 'control-deny-btn', onSelect: () => handleDecision(decisions().negative) }}
          positiveAction={{ label: codexDecisionLabel(decisions().positive), testId: 'control-allow-btn', onSelect: handleAllow }}
          allowChoicePill={allowChoicePill}
          permissionPill={() => buildSessionPermissionPill(props.presets, permissionChoice)}
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
