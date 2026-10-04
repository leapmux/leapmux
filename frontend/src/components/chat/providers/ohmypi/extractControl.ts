import type { DialogPrompt } from '../../model/controlPrompt'
import type { ExtractedControlRequest } from '../capabilities'
import type { ControlExtractionInput } from '../registry'
import { OH_MY_PI_APPROVAL_DIALOG, OH_MY_PI_DIALOG_METHOD, OH_MY_PI_EVENT, OH_MY_PI_FRAME_FIELD } from '~/generated/contracts/ohmypi-protocol'
import { pickNumber, pickObject, pickString } from '~/lib/jsonPick'
import { KIND_ALLOW_ONCE, KIND_REJECT_ONCE } from '../../model/controlPrompt'
import { isOhMyPiApproval } from './controlResponse'

/** The detail line of an approval title that states the command a `bash` call runs. */
const COMMAND_LABEL = 'Command: '
/** The section omp's extension wrapper appends below the details. */
const SAFETY_CHECKS = '\nProvider safety checks:'

/**
 * The arguments of the call one approval asks about, from the call's own start frame.
 *
 * The worker points the control request at that frame (`SourceSeq`) when exactly one
 * call of the tool runs, so the banner states the call's real arguments rather than
 * the lines omp shortened into the dialog's title.
 */
function sourceArgs(input: ControlExtractionInput, toolName: string): Record<string, unknown> | null {
  const frame = input.source?.parentObject
  if (!frame || frame.type !== OH_MY_PI_EVENT.ToolExecutionStart || pickString(frame, OH_MY_PI_FRAME_FIELD.ToolName) !== toolName)
    return null
  return pickObject(frame, OH_MY_PI_FRAME_FIELD.Args)
}

/** Where the command line of an approval body starts, or -1 when it states none. */
function commandStart(body: string): number {
  if (body.startsWith(COMMAND_LABEL))
    return 0
  const index = body.indexOf(`\n${COMMAND_LABEL}`)
  return index < 0 ? -1 : index + 1
}

/**
 * The command a `bash` approval states in its title, and the lines around it.
 *
 * omp writes the command as the LAST detail, `Command: <command>`, and a command can
 * hold newlines of its own, so the command runs to the end of the title -- or to the
 * safety-check section that omp's extension wrapper appends below the details.
 */
function splitCommand(body: string): { command?: string, rest: string } {
  const start = commandStart(body)
  if (start < 0)
    return { rest: body }
  const after = body.slice(start + COMMAND_LABEL.length)
  const end = after.indexOf(SAFETY_CHECKS)
  const command = end < 0 ? after : after.slice(0, end)
  const rest = [body.slice(0, start), end < 0 ? '' : after.slice(end)].join('').trim()
  return { command, rest }
}

/** The control and the heading of each dialog method that draws as a dialog. */
const DIALOG_VARIANTS: Readonly<Record<string, { variant: DialogPrompt['variant'], defaultTitle: string }>> = {
  [OH_MY_PI_DIALOG_METHOD.Confirm]: { variant: 'confirm', defaultTitle: 'Confirm' },
  [OH_MY_PI_DIALOG_METHOD.Input]: { variant: 'input', defaultTitle: 'Enter a value' },
  [OH_MY_PI_DIALOG_METHOD.Editor]: { variant: 'editor', defaultTitle: 'Enter your response' },
}

/**
 * The dialog one extension request asks, or null for a request that is no dialog.
 *
 * omp states each field a dialog carries: a confirm's message, an input's hint, an
 * editor's draft, and the deadline after which omp answers the dialog itself.
 */
function ohMyPiDialog(payload: Record<string, unknown>): DialogPrompt | null {
  if (payload.type !== OH_MY_PI_EVENT.ExtensionUIRequest)
    return null
  const method = pickString(payload, 'method')
  const shape = Object.hasOwn(DIALOG_VARIANTS, method) ? DIALOG_VARIANTS[method] : undefined
  if (!shape)
    return null
  const message = pickString(payload, 'message', undefined)
  const placeholder = pickString(payload, 'placeholder', undefined)
  const prefill = pickString(payload, 'prefill', undefined)
  const timeout = pickNumber(payload, 'timeout')
  return {
    title: pickString(payload, 'title') || shape.defaultTitle,
    ...(message !== undefined ? { message } : {}),
    ...(placeholder !== undefined ? { placeholder } : {}),
    ...(prefill !== undefined ? { prefill } : {}),
    variant: shape.variant,
    // Zero and below state no deadline: omp then waits with no limit.
    ...(timeout != null && timeout > 0 ? { timeoutMs: timeout } : {}),
  }
}

/**
 * Read one omp control request into the shared control model.
 *
 * omp asks for a tool approval with a `select` dialog titled `Allow tool: <name>`, whose
 * further lines state the origin, the reason and the call's details, and whose two
 * options are `Approve` and `Deny` (`tools/approval.ts`). It offers no "always" answer.
 * A `confirm`, an `input` and an `editor` that an extension raises draw as a dialog.
 * Every other request is a question (see `askUserQuestion.ts`), so this answers null
 * for it.
 */
export function ohMyPiExtractControl(input: ControlExtractionInput): ExtractedControlRequest | null {
  const payload = input.payload
  if (!isOhMyPiApproval(payload)) {
    const dialog = ohMyPiDialog(payload)
    return dialog ? { kind: 'dialog', dialog } : null
  }
  const title = pickString(payload, 'title')
  const newline = title.indexOf('\n')
  const first = newline < 0 ? title : title.slice(0, newline)
  const body = newline < 0 ? '' : title.slice(newline + 1)
  const toolName = first.slice(OH_MY_PI_APPROVAL_DIALOG.TitlePrefix.length).trim()
  const args = sourceArgs(input, toolName)
  const fromTitle = splitCommand(body)
  const command = pickString(args, 'command') || fromTitle.command
  const reason = fromTitle.rest.trim()
  return {
    kind: 'permission',
    permission: {
      ...(toolName ? { title: toolName } : {}),
      ...(reason ? { reason } : {}),
      ...(command ? { command } : {}),
      ...(args ? { input: args } : {}),
      options: [
        { optionId: OH_MY_PI_APPROVAL_DIALOG.Approve, kind: KIND_ALLOW_ONCE, name: OH_MY_PI_APPROVAL_DIALOG.Approve },
        { optionId: OH_MY_PI_APPROVAL_DIALOG.Deny, kind: KIND_REJECT_ONCE, name: OH_MY_PI_APPROVAL_DIALOG.Deny },
      ],
    },
  }
}
