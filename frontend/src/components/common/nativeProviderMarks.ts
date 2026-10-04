import { base64ToUint8Array, uint8ArrayToBase64 } from '~/lib/base64'
import { sniffImageDimensionsFromBase64 } from '~/lib/imageDimensions'
import commandCode from '../../../../icons/agents/command-code.svg?raw'
import deepseekHarness from '../../../../icons/agents/deepseek-harness.svg?raw'
import geminiCli from '../../../../icons/agents/gemini-cli.svg?raw'

export interface NativeProviderMark {
  viewBox: string
  fill?: string
  elements: Array<{ type: 'path' | 'image', attributes: Record<string, string> }>
}

const ROOT_ATTRIBUTES = new Set(['xmlns', 'xmlns:xlink', 'viewBox', 'width', 'height', 'fill'])
const ELEMENT_ATTRIBUTES = {
  path: new Set(['d', 'fill', 'fill-rule', 'clip-rule', 'fill-opacity']),
  image: new Set(['width', 'height', 'href']),
}

/** SVG uses decimal numbers with optional exponents. JavaScript also accepts unsupported hexadecimal numbers. */
function svgNumber(value: string | undefined): number {
  if (value === undefined || !/^[+-]?(?:\d+(?:\.\d*)?|\.\d+)(?:e[+-]?\d+)?$/i.test(value.trim()))
    return Number.NaN
  return Number(value)
}

/** Validate canonical bytes and a PNG header. The native asset digest test verifies the complete source image. */
function hasNativePngHeader(href: string | undefined): boolean {
  const prefix = 'data:image/png;base64,'
  if (!href?.startsWith(prefix))
    return false
  const payload = href.slice(prefix.length)
  let bytes: Uint8Array
  try {
    bytes = base64ToUint8Array(payload)
  }
  catch {
    return false
  }
  if (uint8ArrayToBase64(bytes) !== payload)
    return false
  const signature = [137, 80, 78, 71, 13, 10, 26, 10]
  if (signature.some((byte, index) => bytes[index] !== byte))
    return false
  return sniffImageDimensionsFromBase64(payload) !== null
}

function readAttributes(source: string, allowed: ReadonlySet<string>): Record<string, string> {
  const attributes: Record<string, string> = {}
  let cursor = 0
  for (const match of source.matchAll(/([\w:-]+)="([^"]*)"/g)) {
    const name = match[1]!
    if (source.slice(cursor, match.index).trim() || !allowed.has(name) || Object.hasOwn(attributes, name))
      throw new Error('The native provider mark contains an unsupported or repeated attribute.')
    attributes[name] = match[2]!
    cursor = match.index + match[0].length
  }
  if (source.slice(cursor).trim())
    throw new Error('The native provider mark contains an incomplete attribute.')
  return attributes
}

/** Read the project's path and image assets without a DOM or raw HTML rendering. */
export function readNativeProviderMark(source: string): NativeProviderMark {
  const root = /^(?:\s*<!--[\s\S]*?-->)*\s*<svg\s+([^\s<>][^<>]*)>([\s\S]*)<\/svg>\s*$/.exec(source)
  if (!root?.[1] || !root[2])
    throw new Error('The native provider mark requires an SVG root with a viewBox and content.')
  const attributes = readAttributes(root[1], ROOT_ATTRIBUTES)
  const viewBox = attributes.viewBox ?? ''
  const dimensions = viewBox.trim().split(/\s+/).map(svgNumber)
  if (dimensions.length !== 4 || !dimensions.every(Number.isFinite)
    || dimensions[0] !== 0 || dimensions[1] !== 0 || dimensions[2]! <= 0 || dimensions[2] !== dimensions[3]) {
    throw new Error('The native provider mark requires a positive square viewBox at the origin.')
  }
  const body = root[2]
  const elements: NativeProviderMark['elements'] = []
  let cursor = 0
  for (const element of body.matchAll(/<(path|image)\s+([^\s<>][^<>]*)\/>/g)) {
    if (body.slice(cursor, element.index).trim())
      throw new Error('The native provider mark contains unsupported SVG content.')
    const type = element[1] === 'path' ? 'path' : 'image'
    const fields = readAttributes(element[2]!, ELEMENT_ATTRIBUTES[type])
    if (type === 'path' && !fields.d?.trim())
      throw new Error('The native provider path requires its complete geometry.')
    if (type === 'image' && (!hasNativePngHeader(fields.href)
      || !Number.isFinite(svgNumber(fields.width)) || svgNumber(fields.width) <= 0
      || !Number.isFinite(svgNumber(fields.height)) || svgNumber(fields.height) <= 0)) {
      throw new Error('The native provider image requires canonical base64, a PNG header, and positive dimensions.')
    }
    elements.push({ type, attributes: fields })
    cursor = element.index + element[0].length
  }
  if (body.slice(cursor).trim() || elements.length === 0)
    throw new Error('The native provider mark contains unsupported SVG content.')
  return { viewBox, elements, ...(attributes.fill === undefined ? {} : { fill: attributes.fill }) }
}

export const NATIVE_PROVIDER_MARKS = {
  commandCode: readNativeProviderMark(commandCode),
  deepseekHarness: readNativeProviderMark(deepseekHarness),
  geminiCli: readNativeProviderMark(geminiCli),
} satisfies Record<string, NativeProviderMark>
