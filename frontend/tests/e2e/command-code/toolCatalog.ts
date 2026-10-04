import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import { isObject } from '../../../src/lib/jsonPick'
import { nativeModelToolNames } from '../helpers/nativeScenario'

/** Read the complete native attached and deferred tool catalog. */
export function commandCodeToolCatalog(request: MockModelRequestRecord): string[] {
  if (request.protocol !== 'openai-chat-completions' || !isObject(request.body) || !Array.isArray(request.body.messages))
    throw new Error('The Command Code catalog requires a native Chat Completions request.')
  const system = request.body.messages.filter(isObject).filter(message => message.role === 'system').map((message) => {
    if (typeof message.content !== 'string')
      throw new Error('The Command Code system message must contain native text.')
    return message.content
  }).join('\n')
  const additional = system.slice(system.indexOf('# Additional tools'))
  const count = /You HAVE the (\d+) tools below/.exec(additional)?.[1]
  const deferred = [...additional.matchAll(/^- (\w+)\{/gm)].map(match => match[1]!)
  if (count === undefined || deferred.length !== Number(count) || new Set(deferred).size !== deferred.length)
    throw new Error('The Command Code deferred catalog is absent or incomplete.')
  const attached = nativeModelToolNames(request)
  if (attached.some(name => deferred.includes(name)))
    throw new Error('The native Command Code catalog repeats an attached tool.')
  return [...attached, ...deferred]
}
