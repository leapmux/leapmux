import type { NumberedFileLine, ReadFileResult } from '../../../model/readFileResult'
import type { ReadRequest } from '../../../model/tools/read'
import type { AntigravityStep } from './toolCommon'
import type { ImageResultSource } from '~/lib/imageBlocks'
import { parseImageBlock, withFallbackFilePath } from '~/lib/imageBlocks'
import { pickObject, pickString } from '~/lib/jsonPick'
import { fileUriToPath } from '~/lib/paths'

export function antigravityReadRequest(step: AntigravityStep): ReadRequest {
  const view = pickObject(step.raw, 'viewFile')
  const binaryURI = pickString(pickObject(view, 'binaryData'), 'uri') || pickString(pickObject(view, 'mediaData'), 'uri')
  const uri = pickString(view, 'absolutePathUri') || binaryURI
  return { path: pickString(step.args, 'Path') || fileUriToPath(uri) || uri }
}

export function antigravityReadResult(step: AntigravityStep): ReadFileResult {
  const content = pickString(pickObject(step.raw, 'viewFile'), 'content')
  const body = content.replace(/^Showing lines \d+-\d+ of \d+\n?/, '')
  if (!body)
    return { lines: [], fallbackContent: '' }
  const lines: NumberedFileLine[] = []
  for (const line of body.split('\n')) {
    const match = /^(\d+): ?(.*)$/.exec(line)
    if (!match?.[1])
      return { lines: null, fallbackContent: body }
    lines.push({
      num: Number(match[1]),
      text: match[2] ?? '',
    })
  }
  return { lines, fallbackContent: body }
}

export function antigravityReadImages(step: AntigravityStep): ImageResultSource[] {
  const view = pickObject(step.raw, 'viewFile')
  const binary = pickObject(view, 'binaryData')
  const media = pickObject(view, 'mediaData')
  const data = pickString(binary, 'base64Data') || pickString(media, 'inlineData')
  if (!data)
    return []
  const uri = pickString(binary, 'uri') || pickString(media, 'uri') || pickString(view, 'absolutePathUri')
  const image = parseImageBlock({
    type: 'image',
    data,
    mimeType: pickString(binary, 'mimeType') || pickString(media, 'mimeType'),
  })
  return image
    ? [
        withFallbackFilePath(image, fileUriToPath(uri) ?? uri),
      ]
    : []
}
