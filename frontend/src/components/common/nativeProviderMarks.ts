import { base64ToUint8Array, uint8ArrayToBase64 } from '~/lib/base64'
import { sniffImageDimensionsFromBase64 } from '~/lib/imageDimensions'
import commandCode from '../../../../icons/agents/command-code.svg?raw'
import deepseekHarness from '../../../../icons/agents/deepseek-harness.svg?raw'
import geminiCli from '../../../../icons/agents/gemini-cli.svg?raw'
import museCode from '../../../../icons/agents/muse-code.svg?raw'

export interface NativeProviderMark {
  viewBox: string
  fill?: string
  elements: Array<{ type: 'path' | 'image', attributes: Record<string, string> }>
  linearGradients?: NativeLinearGradient[]
}

export interface NativeLinearGradient {
  id: string
  attributes: Record<string, string>
  stops: Record<string, string>[]
}

const ROOT_ATTRIBUTES = new Set(['xmlns', 'xmlns:xlink', 'viewBox', 'width', 'height', 'fill'])
const ELEMENT_ATTRIBUTES = {
  path: new Set(['d', 'fill', 'fill-rule', 'clip-rule', 'fill-opacity']),
  image: new Set(['width', 'height', 'href']),
}
const GRADIENT_ATTRIBUTES = new Set(['id', 'x1', 'y1', 'x2', 'y2', 'gradientUnits'])
const STOP_ATTRIBUTES = new Set(['offset', 'stop-color', 'stop-opacity', 'style'])
const GRADIENT_ID = /^[a-z_][\w:.-]*$/i
const LOCAL_PAINT = /^url\(#([a-z_][\w:.-]*)\)$/i
const HEX_COLOR = /^#(?:[\da-f]{3,4}|[\da-f]{6}|[\da-f]{8})$/i

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

function stopColor(value: string): boolean {
  if (HEX_COLOR.test(value))
    return true
  const p3 = /^color\(display-p3 ([^\s()]+) ([^\s()]+) ([^\s()]+)\)$/.exec(value)
  return p3 !== null && p3.slice(1).every(component => unitNumber(component))
}

/** Gradient colors and opacity restrict each component to the inclusive range from zero to one. */
function unitNumber(value: string | undefined): boolean {
  const number = svgNumber(value)
  return Number.isFinite(number) && number >= 0 && number <= 1
}

function stopStyle(value: string): boolean {
  const declarations = value.split(';')
  if (declarations.at(-1)?.trim() === '')
    declarations.pop()
  if (declarations.length === 0)
    return false
  let colors = 0
  let opacity = false
  for (const declaration of declarations) {
    const colon = declaration.indexOf(':')
    if (colon < 0)
      return false
    const property = declaration.slice(0, colon).trim()
    const color = declaration.slice(colon + 1).trim()
    if (color === '' || (property !== 'stop-color' && property !== 'stop-opacity'))
      return false
    if (property === 'stop-opacity') {
      if (opacity || !unitNumber(color))
        return false
      opacity = true
      continue
    }
    if (!stopColor(color) || colors >= 2 || (colors === 1 && !color.startsWith('color(display-p3 ')))
      return false
    colors++
  }
  return true
}

function gradientOffset(value: string | undefined): number {
  const percent = value?.endsWith('%') === true
  return svgNumber(percent ? value!.slice(0, -1) : value) / (percent ? 100 : 1)
}

function readLinearGradients(source: string): NativeLinearGradient[] {
  const gradients: NativeLinearGradient[] = []
  const ids = new Set<string>()
  let cursor = 0
  for (const match of source.matchAll(/<linearGradient\s+([^\s<>][^<>]*)>([\s\S]*?)<\/linearGradient>/g)) {
    if (source.slice(cursor, match.index).trim())
      throw new Error('The native provider mark contains unsupported SVG content.')
    const { id, ...attributes } = readAttributes(match[1]!, GRADIENT_ATTRIBUTES)
    if (!id || !GRADIENT_ID.test(id) || ids.has(id))
      throw new Error('The native provider gradient requires a unique local ID.')
    for (const coordinate of ['x1', 'y1', 'x2', 'y2']) {
      const value = attributes[coordinate]
      if (value !== undefined && !Number.isFinite(svgNumber(value.endsWith('%') ? value.slice(0, -1) : value)))
        throw new Error('The native provider gradient contains an invalid coordinate.')
    }
    if (attributes.gradientUnits !== undefined && attributes.gradientUnits !== 'userSpaceOnUse' && attributes.gradientUnits !== 'objectBoundingBox')
      throw new Error('The native provider gradient contains unsupported units.')
    const stops: Record<string, string>[] = []
    const body = match[2]!
    let stopCursor = 0
    let previousOffset = -1
    for (const stop of body.matchAll(/<stop\s+([^\s<>][^<>]*)\/>/g)) {
      if (body.slice(stopCursor, stop.index).trim())
        throw new Error('The native provider mark contains unsupported SVG content.')
      const fields = readAttributes(stop[1]!, STOP_ATTRIBUTES)
      const offset = gradientOffset(fields.offset)
      if (!Number.isFinite(offset) || offset < 0 || offset > 1 || offset < previousOffset
        || (fields['stop-color'] !== undefined && !stopColor(fields['stop-color']))
        || (fields['stop-opacity'] !== undefined && !unitNumber(fields['stop-opacity']))
        || (fields.style !== undefined && !stopStyle(fields.style))) {
        throw new Error('The native provider gradient contains an invalid stop.')
      }
      stops.push(fields)
      previousOffset = offset
      stopCursor = stop.index + stop[0].length
    }
    if (body.slice(stopCursor).trim() || stops.length === 0)
      throw new Error('The native provider gradient requires complete stops.')
    gradients.push({ id, attributes, stops })
    ids.add(id)
    cursor = match.index + match[0].length
  }
  if (source.slice(cursor).trim() || gradients.length === 0)
    throw new Error('The native provider mark contains unsupported SVG content.')
  return gradients
}

function validatePaint(value: string | undefined, gradients: readonly NativeLinearGradient[]): void {
  if (value === undefined || value === 'none' || value === 'currentColor' || HEX_COLOR.test(value))
    return
  const local = LOCAL_PAINT.exec(value)
  if (!local || !gradients.some(gradient => gradient.id === local[1]))
    throw new Error('The native provider paint requires a declared local gradient.')
}

/** Read the project's checked SVG assets without a DOM or raw HTML rendering. */
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
  let body = root[2]
  let linearGradients: NativeLinearGradient[] = []
  const definitions = [...body.matchAll(/<defs>([\s\S]*?)<\/defs>/g)]
  if (definitions.length > 1)
    throw new Error('The native provider mark contains repeated definitions.')
  if (definitions.length === 1) {
    const definitionsMatch = definitions[0]!
    linearGradients = readLinearGradients(definitionsMatch[1]!)
    body = body.slice(0, definitionsMatch.index) + body.slice(definitionsMatch.index + definitionsMatch[0].length)
  }
  validatePaint(attributes.fill, linearGradients)
  const elements: NativeProviderMark['elements'] = []
  let cursor = 0
  for (const element of body.matchAll(/<(path|image)\s+([^\s<>][^<>]*)\/>/g)) {
    if (body.slice(cursor, element.index).trim())
      throw new Error('The native provider mark contains unsupported SVG content.')
    const type = element[1] === 'path' ? 'path' : 'image'
    const fields = readAttributes(element[2]!, ELEMENT_ATTRIBUTES[type])
    if (type === 'path' && !fields.d?.trim())
      throw new Error('The native provider path requires its complete geometry.')
    validatePaint(fields.fill, linearGradients)
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
  return {
    viewBox,
    elements,
    ...(attributes.fill === undefined ? {} : { fill: attributes.fill }),
    ...(linearGradients.length === 0 ? {} : { linearGradients }),
  }
}

export const NATIVE_PROVIDER_MARKS = {
  commandCode: readNativeProviderMark(commandCode),
  deepseekHarness: readNativeProviderMark(deepseekHarness),
  geminiCli: readNativeProviderMark(geminiCli),
  museCode: readNativeProviderMark(museCode),
} satisfies Record<string, NativeProviderMark>
