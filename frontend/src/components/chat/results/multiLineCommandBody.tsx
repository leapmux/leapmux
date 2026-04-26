/* eslint-disable solid/no-innerhtml -- HTML is produced via shiki, not arbitrary user input */
import type { JSX } from 'solid-js'
import { renderBashHighlight } from '../toolRenderers'
import { toolResultContentAnsi } from '../toolStyles.css'

/** True when the command spans more than one line (the trigger for the expandable body). */
export function isMultiLineCommand(command: string | null | undefined): boolean {
  return !!command && command.includes('\n')
}

/** Shiki-highlighted multi-line bash command body shared by Claude Bash and ACP execute renderers. */
export function MultiLineCommandBody(props: { command: string }): JSX.Element {
  return <div class={toolResultContentAnsi} innerHTML={renderBashHighlight(props.command)} />
}
