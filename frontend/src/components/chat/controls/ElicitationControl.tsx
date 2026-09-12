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
import { actionButtonClass, ControlActionRow } from './ControlActionRow'
import { ControlJson } from './ControlJson'
import { ControlAllowChoicePillGroup } from './ControlPillGroups'
import { buildElicitationResponse, createElicitationForm, ELICITATION_ACCEPT_CHOICE, elicitationAcceptMetadata, elicitationFieldKey, elicitationURL } from './elicitationForm'
import { createControlChoice, sendResponse } from './types'

export const ElicitationContent: Component<ContentProps & { elicitation: ElicitationRequest }> = (props) => {
  const form = createMemo(() => createElicitationForm(props.elicitation.schema))
  const values = () => props.answerState.choices()
  const validation = createMemo(() => form().read(values()))
  const setValue = (key: string, value: string) => props.answerState.setChoices(previous => ({ ...previous, [elicitationFieldKey(key)]: value }))
  return (
    <>
      <div class={styles.controlBannerTitle}>{props.elicitation.title || 'Input Requested'}</div>
      <Show when={props.elicitation.server}><div>{props.elicitation.server}</div></Show>
      <MarkdownText text={props.elicitation.message} />
      <Show when={props.elicitation.description}><MarkdownText text={props.elicitation.description!} /></Show>
      <ControlJson value={props.elicitation.arguments} hideEmpty />
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
                            disabled={props.optionsDisabled}
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
    catch {
      setPending(false)
      setError('Could not send the response. Try again.')
    }
  }
  return (
    <ControlActionRow
      leading={(
        <>
          <Show when={choicePill()}>{pill => <ControlAllowChoicePillGroup pill={pill()} />}</Show>
          <Show when={error()}><span role="alert">{error()}</span></Show>
        </>
      )}
      primary={(
        <>
          <button class={actionButtonClass(true)} disabled={pending()} onClick={() => void respond(MCP_ELICITATION_ACTION.Cancel)}>Cancel</button>
          <button class={actionButtonClass(true)} disabled={pending()} onClick={() => void respond(MCP_ELICITATION_ACTION.Decline)}>Reject</button>
          <button class={actionButtonClass()} disabled={pending() || !canAccept()} onClick={() => void respond(MCP_ELICITATION_ACTION.Accept)}>Approve</button>
        </>
      )}
    />
  )
}
