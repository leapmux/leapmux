import type { DefaultTreeAdapterTypes } from 'parse5'
import type { QuestionOption } from '../../../model/question'
import { parseFragment } from 'parse5'
import { literalCodeBlock } from '~/lib/literalCodeBlock'

type MarkupNode = DefaultTreeAdapterTypes.Node

const SKIPPED = new Set([
  'input',
  'select',
  'textarea',
  'button',
  'head',
  'iframe',
  'link',
  'meta',
  'noscript',
  'script',
  'style',
  'template',
  'title',
  'svg',
])
const BLOCKS = new Set([
  'address',
  'article',
  'aside',
  'blockquote',
  'dd',
  'details',
  'div',
  'dl',
  'dt',
  'figure',
  'figcaption',
  'footer',
  'header',
  'li',
  'main',
  'nav',
  'p',
  'section',
  'h1',
  'h2',
  'h3',
  'h4',
  'h5',
  'h6',
  'summary',
])

function children(node: MarkupNode): MarkupNode[] {
  return 'childNodes' in node
    ? node.childNodes
    : []
}

function tag(node: MarkupNode): string {
  return 'tagName' in node
    ? node.tagName
    : ''
}

function oneLine(text: string): string {
  return text.replace(/\s+/g, ' ').trim()
}

/** Traverse the parsed nodes without browser APIs or recursive call depth. */
function nodesBelow(root: MarkupNode, skip: (node: MarkupNode) => boolean): MarkupNode[] {
  const result: MarkupNode[] = []
  const pending = [
    ...children(root),
  ].reverse()
  while (pending.length > 0) {
    const node = pending.pop()
    if (!node || skip(node))
      continue
    result.push(node)
    const nested = children(node)
    for (let index = nested.length - 1; index >= 0; index--) {
      const child = nested[index]
      if (child)
        pending.push(child)
    }
  }
  return result
}

function plainText(root: MarkupNode, omitted: ReadonlySet<MarkupNode> = new Set()): string {
  const parts: string[] = []
  for (const node of nodesBelow(root, node => omitted.has(node) || SKIPPED.has(tag(node)))) {
    if ('value' in node && node.nodeName === '#text') {
      parts.push(node.value)
    }
    else if (tag(node) === 'img' && 'attrs' in node) {
      parts.push(node.attrs.find(attr => attr.name === 'alt')?.value ?? '')
    }
    else if (tag(node) === 'br' || BLOCKS.has(tag(node))) {
      parts.push(' ')
    }
  }
  return oneLine(parts.join(''))
}

function codeText(root: MarkupNode): string {
  return nodesBelow(root, () => false).flatMap(node => 'value' in node && node.nodeName === '#text'
    ? [node.value]
    : []).join('')
}

/** Extract presentation from retained label markup; the worker owns the reply strings. */
export function ohMyPiChoiceMarkup(markup: string, fallback: string): Pick<QuestionOption, 'label' | 'description' | 'preview'> {
  if (!markup)
    return { label: fallback }
  const fragment = parseFragment(markup)
  const label = nodesBelow(fragment, node => SKIPPED.has(tag(node))).find(node => tag(node) === 'label')
  if (!label)
    return { label: fallback }
  const visible = nodesBelow(label, node => SKIPPED.has(tag(node)))
  const code = visible.filter(node => tag(node) === 'pre')
  const omitted = new Set(code)
  const first = children(label).find((node) => {
    if (node.nodeName === '#comment' || SKIPPED.has(tag(node)))
      return false
    if (node.nodeName !== '#text')
      return true
    return 'value' in node && node.value.trim() !== ''
  })
  const heading = first && (tag(first) === 'strong' || tag(first) === 'b')
    ? first
    : undefined
  const title = heading
    ? plainText(heading)
    : plainText(label, omitted)
  if (heading)
    omitted.add(heading)
  const description = heading
    ? plainText(label, omitted)
    : ''
  const preview = code.map(codeText).filter(text => text.trim() !== '').map(literalCodeBlock).join('\n\n')
  const nativeText = plainText(label)
  const suffix = nativeText && fallback.startsWith(nativeText)
    ? fallback.slice(nativeText.length)
    : ''
  const disambiguation = /^ \((?:[2-9]|[1-9]\d+)\)$/.test(suffix)
    ? suffix
    : ''
  return {
    label: title
      ? title + disambiguation
      : fallback,
    ...(description
      ? { description }
      : {}),
    ...(preview
      ? { preview }
      : {}),
  }
}
