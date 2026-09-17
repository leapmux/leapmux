import type { ParentComponent } from 'solid-js'
import type { PermissionRequestIR } from '../ir/controlRequest'
import type { ControlRequest } from '~/stores/control.store'
import { createMemo, Show } from 'solid-js'
import { isObject } from '~/lib/jsonPick'
import * as styles from '../ControlRequestBanner.css'
import { CollapsibleText } from './CollapsibleText'
import { ControlJson } from './ControlJson'
import { canAnswerControlRequest } from './controlResponseState'

/**
 * The permission body: what the call is, why it needs approval, and its arguments.
 *
 * It draws every field of the IR's permission EXCEPT the options, which are the
 * answers -- those belong to the actions half, beside the buttons that send one.
 * The elicitation form draws the same body for an input request, which states no
 * options at all.
 */
export const PermissionRequestContent: ParentComponent<{
  request: ControlRequest
  source: Omit<PermissionRequestIR, 'options'>
}> = (props) => {
  const details = createMemo(() => {
    const { input, command } = props.source
    if (command === undefined || !isObject(input) || input.command !== command)
      return input
    return Object.fromEntries(Object.entries(input).filter(([key]) => key !== 'command'))
  })
  return (
    <>
      <div class={styles.controlBannerTitle}>{canAnswerControlRequest(props.request) ? 'Permission Required' : 'Permission request'}</div>
      <Show when={props.source.title}>
        <div class={styles.bannerHint}>{props.source.title}</div>
      </Show>
      <Show when={props.source.reason}>
        <div class={styles.bannerReason}>{props.source.reason}</div>
      </Show>
      {props.children}
      <Show when={props.source.command !== undefined}>
        <CollapsibleText text={props.source.command!} maxLines={6} class={styles.bannerCodeBlock} />
      </Show>
      <ControlJson value={details()} hideEmpty />
      <Show when={props.source.workingDirectory}>
        <div class={styles.bannerHint}>
          Directory:
          {' '}
          {props.source.workingDirectory}
        </div>
      </Show>
    </>
  )
}
