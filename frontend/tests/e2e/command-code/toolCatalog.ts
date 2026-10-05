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

/**
 * Read the names of the tool schemas that one native `load_tools` result loaded.
 *
 * The native tool answers a `select:<name>` request with a fuzzy search that keeps the best match of each name
 * (Command Code 1.74.1, createSearchToolsTool). A name that the catalog lacks can load a tool with another name,
 * so a lookup proves that a tool is absent only when no loaded name equals it.
 *
 * The result states how many schemas it loaded, and each schema has one `### <name>` header. A header count that
 * differs from that number means that the header form changed, and a lookup of an absent tool would then pass on
 * an empty list. So this reader throws instead of returning a list that it cannot trust.
 */
export function commandCodeLoadedToolNames(result: string): string[] {
  const names = [...result.matchAll(/^### (\S+)$/gm)].map(match => match[1]!)
  const stated = /^Loaded (\d+) tool schema\(s\)/u.exec(result)?.[1]
  if (names.length !== Number(stated ?? 0))
    throw new Error(`The Command Code load_tools result states ${stated ?? 'no'} loaded schemas, and ${names.length} headers name one.`)
  return names
}
