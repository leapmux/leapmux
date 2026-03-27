import type { Component } from 'solid-js'
import type { ActionsProps, AskQuestionState, ContentProps, Question } from './types'

import { For, Match, Show, Switch } from 'solid-js'
import { ButtonGroup } from '~/components/common/ButtonGroup'
import * as styles from '../ControlRequestBanner.css'
import { AskUserQuestionActions, AskUserQuestionContent } from './AskUserQuestionControl'
import { toRpcId } from './CodexControlRequest'
import { sendResponse } from './types'

/** Extract OpenCode requestPermission params from the control request payload. */
function getOpenCodeParams(payload: Record<string, unknown>): Record<string, unknown> | undefined {
  return payload.params as Record<string, unknown> | undefined
}

/** Extract the tool call info from a requestPermission payload. */
function getToolCall(payload: Record<string, unknown>): Record<string, unknown> | undefined {
  const params = getOpenCodeParams(payload)
  return params?.toolCall as Record<string, unknown> | undefined
}

/** Extract permission options from a requestPermission payload. */
function getOptions(payload: Record<string, unknown>): Array<{ optionId: string, kind: string, name: string }> {
  const params = getOpenCodeParams(payload)
  return (params?.options as Array<{ optionId: string, kind: string, name: string }>) ?? []
}

function getQuestionProperties(payload: Record<string, unknown>): Record<string, unknown> | undefined {
  return payload.properties as Record<string, unknown> | undefined
}

function isOpenCodeQuestionPayload(payload: Record<string, unknown>): boolean {
  return payload.type === 'question.asked' && Array.isArray(getQuestionProperties(payload)?.questions)
}

function wrapAsAskUserQuestion(payload: Record<string, unknown>): Record<string, unknown> {
  const properties = getQuestionProperties(payload)
  const questions = (properties?.questions as Array<Record<string, unknown>> | undefined) ?? []
  return {
    ...payload,
    request: {
      tool_name: 'AskUserQuestion',
      input: {
        questions: questions.map((question) => {
          const mapped = { ...question }
          if ('multiple' in mapped && !('multiSelect' in mapped))
            mapped.multiSelect = mapped.multiple
          return mapped
        }),
      },
    },
  }
}

/**
 * Sends an OpenCode permission response as a JSON-RPC response.
 */
export function sendOpenCodePermissionResponse(
  agentId: string,
  onRespond: (agentId: string, content: Uint8Array) => Promise<void>,
  requestId: string,
  optionId: string,
): Promise<void> {
  return sendResponse(agentId, onRespond, {
    jsonrpc: '2.0',
    id: toRpcId(requestId),
    result: { outcome: { outcome: 'selected', optionId } },
  })
}

export function sendOpenCodeQuestionResponse(
  agentId: string,
  onRespond: (agentId: string, content: Uint8Array) => Promise<void>,
  requestId: string,
  questions: Question[],
  askState: AskQuestionState,
): Promise<void> {
  const answers: string[][] = questions.map((_, index) => {
    const selected = askState.selections()[index] ?? []
    const customText = askState.customTexts()[index]?.trim()
    if (selected.length > 0)
      return selected
    if (customText)
      return [customText]
    return []
  })
  return sendResponse(agentId, onRespond, {
    jsonrpc: '2.0',
    id: toRpcId(requestId),
    result: { answers },
  })
}

export function sendOpenCodeQuestionRejectResponse(
  agentId: string,
  onRespond: (agentId: string, content: Uint8Array) => Promise<void>,
  requestId: string,
): Promise<void> {
  return sendResponse(agentId, onRespond, {
    jsonrpc: '2.0',
    id: toRpcId(requestId),
    result: { rejected: true },
  })
}

/** OpenCode-specific control request content. */
export const OpenCodeControlContent: Component<ContentProps> = (props) => {
  const toolCall = () => getToolCall(props.request.payload)
  const title = () => (toolCall()?.title as string) || 'Permission Request'
  const kind = () => toolCall()?.kind as string | undefined

  return (
    <Switch>
      <Match when={isOpenCodeQuestionPayload(props.request.payload)}>
        <AskUserQuestionContent
          request={{ ...props.request, payload: wrapAsAskUserQuestion(props.request.payload) }}
          askState={props.askState}
          optionsDisabled={props.optionsDisabled}
        />
      </Match>
      <Match when={true}>
        <>
          <div class={styles.controlBannerTitle}>{title()}</div>
          <Show when={kind()}>
            <div class={styles.codexCwd}>{kind()}</div>
          </Show>
        </>
      </Match>
    </Switch>
  )
}

/** OpenCode-specific control request action buttons. */
export const OpenCodeControlActions: Component<ActionsProps> = (props) => {
  const options = () => getOptions(props.request.payload)
  const questions = () => ((getQuestionProperties(props.request.payload)?.questions as Question[] | undefined) ?? []).map(question => ({
    ...question,
    multiSelect: question.multiSelect ?? (question as Question & { multiple?: boolean }).multiple,
  }))

  const handleOption = (optionId: string) => {
    sendOpenCodePermissionResponse(props.request.agentId, props.onRespond, props.request.requestId, optionId)
  }

  const handleBypassPermissions = () => {
    // Allow once, then switch to bypass mode.
    sendOpenCodePermissionResponse(props.request.agentId, props.onRespond, props.request.requestId, 'once')
    if (props.bypassPermissionMode)
      props.onPermissionModeChange?.(props.bypassPermissionMode)
  }

  const userInputOnRespond = async (_agentId: string, content: Uint8Array) => {
    const parsed = JSON.parse(new TextDecoder().decode(content))
    if (parsed?.response?.response?.behavior === 'deny') {
      await sendOpenCodeQuestionRejectResponse(props.request.agentId, props.onRespond, props.request.requestId)
      return
    }
    await sendOpenCodeQuestionResponse(props.request.agentId, props.onRespond, props.request.requestId, questions(), props.askState)
  }

  return (
    <Switch>
      <Match when={isOpenCodeQuestionPayload(props.request.payload)}>
        <AskUserQuestionActions
          {...props}
          request={{ ...props.request, payload: wrapAsAskUserQuestion(props.request.payload) }}
          onRespond={userInputOnRespond}
        />
      </Match>
      <Match when={true}>
        <div class={styles.controlFooter}>
          <div class={styles.controlFooterLeft}>
            {props.infoTrigger}
          </div>
          <div class={styles.controlFooterRight}>
            <Show
              when={options().length > 0}
              fallback={(
                <ButtonGroup>
                  <button class="outline" onClick={() => handleOption('reject')} data-testid="control-deny-btn">Reject</button>
                  <button onClick={() => handleOption('once')} data-testid="control-allow-btn">Allow once</button>
                  <Show when={props.bypassPermissionMode}>
                    <button
                      data-variant="secondary"
                      onClick={handleBypassPermissions}
                      data-testid="control-bypass-btn"
                      title="Allow this request and stop asking for permissions"
                    >
                      & Bypass Permissions
                    </button>
                  </Show>
                </ButtonGroup>
              )}
            >
              <ButtonGroup>
                <For each={options()}>
                  {option => (
                    <button
                      class={option.kind === 'reject_once' ? 'outline' : undefined}
                      onClick={() => handleOption(option.optionId)}
                      data-testid={`control-decision-${option.optionId}`}
                    >
                      {option.name}
                    </button>
                  )}
                </For>
                <Show when={props.bypassPermissionMode}>
                  <button
                    data-variant="secondary"
                    onClick={handleBypassPermissions}
                    data-testid="control-bypass-btn"
                    title="Allow this request and stop asking for permissions"
                  >
                    & Bypass Permissions
                  </button>
                </Show>
              </ButtonGroup>
            </Show>
          </div>
        </div>
      </Match>
    </Switch>
  )
}
