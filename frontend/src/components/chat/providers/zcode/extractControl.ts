import type { ControlExtractionInput, ExtractedControlRequest } from '../registry'
import { ZCODE_TOOL } from '~/generated/contracts/zcode-protocol'
import { pickObject, pickString } from '~/lib/jsonPick'
import { getToolInput, getToolName } from '~/utils/controlResponse'

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
      options: [],
    },
  }
}
