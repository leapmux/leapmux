import type { Component } from 'solid-js'
import type { ElicitationRequest } from './elicitationForm'
import type { ActionsProps, ContentProps } from './types'
import type { McpElicitationAction } from '~/generated/contracts/mcp-elicitation'
import { createMemo, createSignal, createUniqueId, For, Match, Show, Switch } from 'solid-js'
import { LoadingMenu } from '~/components/common/LoadingMenu'
import { isPillOptions } from '~/components/common/PillGroup'
import { MCP_ELICITATION_ACTION } from '~/generated/contracts/mcp-elicitation'
import { UNTRUSTED_LINK_ATTRIBUTE } from '~/lib/untrustedLinkClicks'
import * as styles from '../ControlRequestBanner.css'
import { MarkdownText } from '../messageRenderers'
import { ControlDecisionFooter } from './ControlDecisionFooter'
import { ControlJson } from './ControlJson'
import { controlResponseErrorMessage, ReportedControlResponseError } from './controlResponseError'
import { buildElicitationResponse, createElicitationForm, ELICITATION_ACCEPT_CHOICE, elicitationAcceptMetadata, elicitationFieldKey, elicitationURL } from './elicitationForm'
import { PermissionRequestContent } from './PermissionRequestContent'
import { createControlChoice, sendResponse } from './types'

export const ElicitationContent: Component<ContentProps & { elicitation: ElicitationRequest }> = (props) => {
  const form = createMemo(() => createElicitationForm(props.elicitation.schema))
  const values = () => props.answerState.choices()
  const validation = createMemo(() => form().read(values()))
  const setValue = (key: string, value: string) => props.answerState.setChoices(previous => ({ ...previous, [elicitationFieldKey(key)]: value }))
  const description = () => (
    <>
      <Show when={props.elicitation.server}><div>{props.elicitation.server}</div></Show>
      <MarkdownText text={props.elicitation.message} />
      <Show when={props.elicitation.description}><MarkdownText text={props.elicitation.description!} /></Show>
    </>
  )
  return (
    <>
      <Show
        when={props.elicitation.purpose === 'permission'}
        fallback={(
          <>
            <div class={styles.controlBannerTitle}>{props.elicitation.title || 'Input Requested'}</div>
            {description()}
            <ControlJson value={props.elicitation.arguments} hideEmpty />
          </>
        )}
      >
        <PermissionRequestContent
          request={props.request}
          source={{
            // The generic title is the banner's own; any other replaces it.
            // Omitted when absent — `Show` reads it the same either way.
            ...(props.elicitation.title === undefined || props.elicitation.title === 'Permission Required' ? {} : { title: props.elicitation.title }),
            input: props.elicitation.arguments,
          }}
        >
          {description()}
        </PermissionRequestContent>
      </Show>
      <Show when={props.elicitation.argumentNotice}><div class={styles.bannerHint}>{props.elicitation.argumentNotice}</div></Show>
      <Switch fallback={<p>This request type is not supported. You can reject or cancel it.</p>}>
        <Match when={props.elicitation.mode === 'form'}>
          <Show when={form().fields.length > 0 || validation().error}>
            <fieldset disabled={props.optionsDisabled} style={{ 'min-width': '0', 'display': 'grid', 'gap': 'var(--space-3, 0.75rem)' }} data-elicitation-form data-testid="elicitation-form">
              <For each={form().fields}>
                {(field) => {
                  const id = createUniqueId()
                  const label = `${field.label || 'Response'}${field.required ? ' *' : ''}`
                  const value = () => values()[elicitationFieldKey(field.key)] ?? field.initial
                  const selected = createMemo(() => {
                    try {
                      const array: unknown = JSON.parse(value() || '[]')
                      return Array.isArray(array) ? array.map(item => JSON.stringify(item)) : []
                    }
                    catch {
                      return []
                    }
                  })
                  return (
                    <div>
                      <Show when={field.type === 'select'} fallback={<label for={id}>{label}</label>}>
                        <div>{label}</div>
                      </Show>
                      <Show when={field.description}><MarkdownText text={field.description} /></Show>
                      <Switch>
                        <Match when={field.type === 'select'}>
                          <LoadingMenu
                            ariaLabel={label}
                            value={value()}
                            options={[{ value: '', label: 'Select an option', pinned: true }, ...field.options]}
                            emptyLabel="No options"
                            {...(props.optionsDisabled === undefined ? {} : { disabled: props.optionsDisabled })}
                            onChange={value => setValue(field.key, value)}
                          />
                        </Match>
                        <Match when={field.type === 'multiple'}>
                          <div id={id} role="group" aria-label={field.label}>
                            <For each={field.options}>
                              {option => (
                                <label>
                                  <input
                                    type="checkbox"
                                    checked={selected().includes(option.value)}
                                    onChange={(event) => {
                                      const next = event.currentTarget.checked ? [...selected(), option.value] : selected().filter(value => value !== option.value)
                                      setValue(field.key, JSON.stringify(next.map(value => JSON.parse(value))))
                                    }}
                                  />
                                  {option.label}
                                </label>
                              )}
                            </For>
                          </div>
                        </Match>
                        <Match when={field.type === 'text' || field.type === 'number'}>
                          <input id={id} type="text" inputMode={field.type === 'number' ? 'decimal' : undefined} value={value()} onInput={event => setValue(field.key, event.currentTarget.value)} />
                        </Match>
                        <Match when={field.type === 'json'}>
                          <textarea id={id} value={value()} spellcheck={false} onInput={event => setValue(field.key, event.currentTarget.value)} />
                        </Match>
                      </Switch>
                    </div>
                  )
                }}
              </For>
              <Show when={validation().error}><div role="status">{validation().error}</div></Show>
            </fieldset>
          </Show>
        </Match>
        <Match when={props.elicitation.mode === 'url'}>
          <Show when={elicitationURL(props.elicitation.url)} fallback={<p>The request contains no usable web link.</p>}>
            {url => <a href={url()} target="_blank" rel="noopener noreferrer nofollow" {...{ [UNTRUSTED_LINK_ATTRIBUTE]: '' }}>{url()}</a>}
          </Show>
        </Match>
      </Switch>
    </>
  )
}

export const ElicitationActions: Component<ActionsProps & { elicitation: ElicitationRequest }> = (props) => {
  const form = createMemo(() => createElicitationForm(props.elicitation.schema))
  const answer = createMemo(() => form().read(props.answerState.choices()))
  const canAccept = () => props.elicitation.mode === 'form' ? !!answer().content : props.elicitation.mode === 'url' && !!elicitationURL(props.elicitation.url)
  const choice = createControlChoice(() => props.answerState, ELICITATION_ACCEPT_CHOICE, 'once')
  const choicePill = createMemo(() => {
    const options = props.elicitation.acceptChoices ?? []
    if (options.length < 2 || !isPillOptions(options))
      return undefined
    return { label: 'Allow as', options, selected: options.some(option => option.key === choice.choice()) ? choice.choice()! : options[0].key, onSelect: choice.setChoice }
  })
  const [pending, setPending] = createSignal(false)
  const [error, setError] = createSignal('')
  const respond = async (action: McpElicitationAction) => {
    if (pending() || (action === MCP_ELICITATION_ACTION.Accept && !canAccept()))
      return
    setPending(true)
    setError('')
    try {
      await sendResponse(props.onRespond, buildElicitationResponse(props.request.requestId, action, props.elicitation.mode === 'form' ? answer().content : undefined, elicitationAcceptMetadata(props.elicitation, props.answerState.choices())))
    }
    catch (error) {
      if (!(error instanceof ReportedControlResponseError))
        setError(controlResponseErrorMessage(error))
    }
    finally {
      // A send that RESOLVES can still leave the request open: the worker
      // records a response it cannot confirm, and the store keeps the request.
      // The reset belonged in the catch alone, so those buttons stayed disabled
      // with no way to answer again. A completed response unmounts this
      // component, where the reset costs nothing.
      setPending(false)
    }
  }
  return (
    <ControlDecisionFooter
      hasEditorContent={false}
      onSendFeedback={props.onTriggerSend}
      allowChoicePill={choicePill}
      error={error()}
      negativeAction={{
        label: props.elicitation.purpose === 'permission' ? 'Deny' : 'Reject',
        testId: 'control-deny-btn',
        disabled: pending(),
        onSelect: () => respond(MCP_ELICITATION_ACTION.Decline),
      }}
      positiveAction={{
        label: props.elicitation.purpose === 'permission' ? 'Allow' : 'Approve',
        testId: 'control-allow-btn',
        disabled: pending() || !canAccept(),
        onSelect: () => respond(MCP_ELICITATION_ACTION.Accept),
      }}
      additionalActions={() => [{ label: 'Cancel', testId: 'control-cancel-btn', disabled: pending(), onSelect: () => respond(MCP_ELICITATION_ACTION.Cancel) }]}
    />
  )
}
