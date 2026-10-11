import type { PermissionOption } from '../../model/controlPrompt'
import type { ControlExtractionInput, ExtractedControlRequest } from '../registry'
import { ZCODE_TOOL } from '~/generated/contracts/zcode-protocol'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'
import { getToolInput, getToolName } from '~/utils/controlResponse'
import { KIND_ALLOW_ALWAYS, KIND_ALLOW_ONCE, KIND_REJECT_ONCE } from '../../model/controlPrompt'

/**
 * `Provider.extractControl` for ZCode.
 *
 * ZCode multiplexes three prompts over two RPCs, and the worker records which one
 * arrived as the request's TOOL NAME -- so the tool name is what this switches on,
 * exactly as Claude's does.
 *
 *   ExitPlanMode     -> the plan approval (interaction/requestUserInput)
 *   anything else    -> a permission      (interaction/requestPermission)
 *
 * `AskUserQuestion` reaches the same two RPCs and is absent from this list on
 * purpose: `askUserQuestion.isRequest` recognizes it, and the control surface asks
 * that before any provider's reader runs.
 */

/** The canonical kind one native option kind belongs to. An unknown kind stays as it arrived, so the row keeps the option in its additional buttons instead of guessing a slot for it. */
function zcodeOptionKind(kind: string): string {
  switch (kind) {
    case 'allow_once': return KIND_ALLOW_ONCE
    case 'allow_always':
    case 'allow_project':
    case 'allow_session': return KIND_ALLOW_ALWAYS
    case 'deny':
    case 'deny_once': return KIND_REJECT_ONCE
    default: return kind
  }
}

/**
 * The native options one permission request offers, in payload order.
 *
 * The app-server embeds the COMPLETE reply of each option -- an "always allow"
 * choice carries its permission-rule updates inside its `response` -- so the ids
 * are what the answer echoes, and the names are the words the pills draw.
 */
export function zcodePermissionOptions(payload: Record<string, unknown>): PermissionOption[] {
  const params = pickObject(payload, 'params')
  const offered = isObject(params) ? params.options : undefined
  if (!Array.isArray(offered))
    return []
  const options: PermissionOption[] = []
  for (const option of offered) {
    if (!isObject(option))
      continue
    const optionId = pickString(option, 'optionId', '')
    if (optionId === '')
      continue
    const name = pickString(option, 'name', undefined)
    options.push(name === undefined
      ? { optionId, kind: zcodeOptionKind(pickString(option, 'kind', '')) }
      : { optionId, kind: zcodeOptionKind(pickString(option, 'kind', '')), name })
  }
  return options
}

export function zcodeExtractControl(input: ControlExtractionInput): ExtractedControlRequest | null {
  const { payload } = input
  const toolName = getToolName(payload)
  if (toolName === ZCODE_TOOL.ExitPlanMode)
    return { kind: 'plan' }
  // A QUESTION never reaches here: `askUserQuestion.isRequest` is the one recognizer,
  // and the control surface answers it before any provider's reader runs.
  const toolInput = getToolInput(payload)
  const reason = pickString(pickObject(payload, 'params'), 'reason', undefined)
  // A shell call states its command, which the banner draws as code above the
  // arguments. One shared body spelled `'Bash'` inline for every provider that
  // reached it; the tool table owns the name here.
  const command = toolName === ZCODE_TOOL.Bash ? pickString(toolInput, 'command', undefined) : undefined
  return {
    kind: 'permission',
    permission: {
      title: toolName,
      // ZCode's own explanation of why the call needs approval, which is the most
      // useful line in the banner -- so it sits above the arguments.
      ...(reason !== undefined ? { reason } : {}),
      input: toolInput,
      ...(command !== undefined ? { command } : {}),
      options: zcodePermissionOptions(payload),
    },
  }
}
