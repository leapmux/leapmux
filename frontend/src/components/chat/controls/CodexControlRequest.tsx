import type { Component } from 'solid-js'
import type { ActionsProps, AskQuestionState, ContentProps, Question } from './types'

import { For, Match, Show, Switch } from 'solid-js'
import { ButtonGroup } from '~/components/common/ButtonGroup'
import { Tooltip } from '~/components/common/Tooltip'
import { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import { buildAllowResponse, buildDenyResponse, getToolInput, getToolName } from '~/utils/controlResponse'
import * as styles from '../ControlRequestBanner.css'
import { AskUserQuestionActions, AskUserQuestionContent } from './AskUserQuestionControl'
import { CollapsibleText } from './CollapsibleText'
import { createPlanApprovalState, PlanApprovalCheckboxes } from './PlanApprovalCheckboxes'
import { sendResponse } from './types'

/** Extract Codex approval params from the control request payload. */
function getCodexParams(payload: Record<string, unknown>): Record<string, unknown> | undefined {
  return payload.params as Record<string, unknown> | undefined
}

type CodexDecision = string | Record<string, unknown>

/** Convert a string request ID to a numeric JSON-RPC id when possible. */
export function toRpcId(requestId: string): number | string {
  const numId = Number(requestId)
  return Number.isFinite(numId) ? numId : requestId
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

/** Label for a Codex decision. */
function decisionLabel(decision: CodexDecision): string {
  if (typeof decision === 'string') {
    switch (decision) {
      case 'accept': return 'Allow'
      case 'acceptForSession': return 'Allow for Session'
      case 'decline': return 'Reject'
      case 'cancel': return 'Cancel'
      default: return decision
    }
  }
  if ('acceptWithExecpolicyAmendment' in decision)
    return 'Allow & Remember'
  if ('applyNetworkPolicyAmendment' in decision)
    return 'Apply Network Policy'
  return 'Allow'
}

/** Whether a decision is a cancel/decline type (rendered as outline button). */
function isNegativeDecision(decision: CodexDecision): boolean {
  return decision === 'decline' || decision === 'cancel'
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

/** Codex plan mode prompt action buttons (with clear context + bypass checkboxes). */
const CodexPlanModePromptActions: Component<ActionsProps> = (props) => {
  const planApproval = createPlanApprovalState(props)

  const handleApprove = () => {
    sendCodexPlanPromptResponse(props.request.agentId, props.onRespond, buildAllowResponse(props.request.requestId, getToolInput(props.request.payload), { permissionMode: planApproval.permissionMode(), clearContext: planApproval.clearContext() }))
  }

  return (
    <div class={styles.controlFooter}>
      <div class={styles.controlFooterLeft}>
        {props.infoTrigger}
      </div>
      <div class={styles.controlFooterRight}>
        <Show when={!props.hasEditorContent}>
          <PlanApprovalCheckboxes state={planApproval} bypassPermissionMode={props.bypassPermissionMode} />
        </Show>
        <button
          class="outline"
          onClick={() => {
            if (props.hasEditorContent) {
              props.onTriggerSend()
              return
            }
            sendCodexPlanPromptResponse(props.request.agentId, props.onRespond, buildDenyResponse(props.request.requestId, ''))
          }}
          data-testid="control-deny-btn"
        >
          {props.hasEditorContent ? 'Send Feedback' : 'Stay in Plan Mode'}
        </button>
        <Show when={!props.hasEditorContent}>
          <button
            onClick={handleApprove}
            data-testid="control-allow-btn"
          >
            Implement Plan
          </button>
        </Show>
      </div>
    </div>
  )
}

/** Codex-specific control request action buttons. */
export const CodexControlActions: Component<ActionsProps> = (props) => {
  const toolName = () => getToolName(props.request.payload)
  const method = () => props.request.payload.method as string | undefined
  const params = () => getCodexParams(props.request.payload)
  const availableDecisions = () => params()?.availableDecisions as CodexDecision[] | undefined
  const questions = () => (params()?.questions as Question[] | undefined) ?? []

  const handleDecision = (decision: CodexDecision) => {
    sendCodexDecision(props.request.agentId, props.onRespond, props.request.requestId, decision)
  }

  const handleBypassPermissions = () => {
    // Accept the current request, then switch to bypass mode.
    sendCodexDecision(props.request.agentId, props.onRespond, props.request.requestId, 'accept')
    if (props.bypassPermissionMode)
      props.onPermissionModeChange?.(props.bypassPermissionMode)
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
        <div class={styles.controlFooter}>
          <div class={styles.controlFooterLeft}>
            {props.infoTrigger}
          </div>
          <div class={styles.controlFooterRight}>
            <Show
              when={availableDecisions()}
              fallback={(
                <ButtonGroup>
                  <button class="outline" onClick={() => handleDecision('cancel')} data-testid="control-deny-btn">Cancel</button>
                  <button onClick={() => handleDecision('accept')} data-testid="control-allow-btn">Allow</button>
                  <Show when={props.bypassPermissionMode}>
                    <Tooltip text="Allow this request and stop asking for permissions">
                      <button
                        data-variant="secondary"
                        onClick={handleBypassPermissions}
                        data-testid="control-bypass-btn"
                      >
                        & Bypass Permissions
                      </button>
                    </Tooltip>
                  </Show>
                </ButtonGroup>
              )}
            >
              {decisions => (
                <ButtonGroup>
                  <For each={decisions()}>
                    {decision => (
                      <button
                        class={isNegativeDecision(decision) ? 'outline' : undefined}
                        onClick={() => handleDecision(decision)}
                        data-testid={`control-decision-${typeof decision === 'string' ? decision : Object.keys(decision)[0]}`}
                      >
                        {decisionLabel(decision)}
                      </button>
                    )}
                  </For>
                  <Show when={props.bypassPermissionMode}>
                    <Tooltip text="Allow this request and stop asking for permissions">
                      <button
                        data-variant="secondary"
                        onClick={handleBypassPermissions}
                        data-testid="control-bypass-btn"
                      >
                        & Bypass Permissions
                      </button>
                    </Tooltip>
                  </Show>
                </ButtonGroup>
              )}
            </Show>
          </div>
        </div>
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
