import { Schema } from '@milkdown/prose/model'
import { describe, expect, it, vi } from 'vitest'
import { isBlankEditorDocument, serializeEditorDocument } from './documentMarkdown'

const schema = new Schema({ nodes: {
  doc: { content: 'block*' },
  paragraph: { content: 'inline*', group: 'block' },
  text: { group: 'inline' },
  hardbreak: { group: 'inline', inline: true },
  image: { group: 'inline', inline: true },
  heading: { content: 'inline*', group: 'block' },
  code_block: { content: 'text*', group: 'block' },
  horizontal_rule: { group: 'block' },
  blockquote: { content: 'block*', group: 'block' },
} })

describe('isBlankEditorDocument', () => {
  it('accepts an empty document and multiple blank paragraphs with hard breaks', () => {
    expect(isBlankEditorDocument(schema.node('doc'))).toBe(true)
    const doc = schema.node('doc', null, [
      schema.node('paragraph'),
      schema.node('paragraph', null, [schema.text(' \t\n\u00A0'), schema.node('hardbreak')]),
    ])
    expect(isBlankEditorDocument(doc)).toBe(true)
    const serialize = vi.fn(() => '<br />')
    expect(serializeEditorDocument(doc, serialize)).toBe('')
    expect(serialize).not.toHaveBeenCalled()
  })

  it.each(['0', '-1', '界', '<br />', 'x'.repeat(10_000)])('preserves visible text: %s', (text) => {
    const doc = schema.node('doc', null, [schema.node('paragraph'), schema.node('paragraph', null, schema.text(text))])
    expect(isBlankEditorDocument(doc)).toBe(false)
    const serialize = vi.fn(() => `\n${text}\n`)
    expect(serializeEditorDocument(doc, serialize)).toBe(`\n${text}\n`)
    expect(serialize).toHaveBeenCalledExactlyOnceWith(doc)
  })

  it.each(['heading', 'code_block', 'horizontal_rule', 'blockquote'])('preserves an empty structured %s block', (type) => {
    expect(isBlankEditorDocument(schema.node('doc', null, schema.node(type)))).toBe(false)
  })

  it('preserves images without text and rejects a non-document node', () => {
    const paragraph = schema.node('paragraph', null, schema.node('image'))
    expect(isBlankEditorDocument(schema.node('doc', null, paragraph))).toBe(false)
    expect(() => isBlankEditorDocument(paragraph)).toThrow('schema document root')
  })

  it('keeps the existing needless-escape correction for actual prose', () => {
    const doc = schema.node('doc', null, schema.node('paragraph', null, schema.text('new.txt')))
    expect(serializeEditorDocument(doc, () => 'new\\.txt')).toBe('new.txt')
  })
})
