/**
 * Map file extensions to Shiki language identifiers.
 * Only includes languages loaded by the shared Shiki highlighter instance.
 */

import { extname } from './paths'

const EXT_TO_LANG: Record<string, string> = {
  // TypeScript / JavaScript
  ts: 'typescript',
  mts: 'typescript',
  cts: 'typescript',
  tsx: 'tsx',
  js: 'javascript',
  mjs: 'javascript',
  cjs: 'javascript',
  jsx: 'jsx',
  // Python
  py: 'python',
  pyi: 'python',
  // Rust
  rs: 'rust',
  // Go
  go: 'go',
  // Java
  java: 'java',
  // Shell
  sh: 'bash',
  bash: 'bash',
  zsh: 'bash',
  // Data formats
  json: 'json',
  jsonc: 'json',
  // Web
  html: 'html',
  htm: 'html',
  css: 'css',
  // Config
  yaml: 'yaml',
  yml: 'yaml',
  toml: 'toml',
  // SQL
  sql: 'sql',
  // Docs
  md: 'markdown',
  markdown: 'markdown',
  mdx: 'markdown',
  // Diff
  diff: 'diff',
  patch: 'diff',
  // Logs (ANSI escape highlighting)
  log: 'ansi',
  // C/C++
  c: 'c',
  h: 'c',
  cpp: 'cpp',
  cxx: 'cpp',
  cc: 'cpp',
  hpp: 'cpp',
  hxx: 'cpp',
  // XML
  xml: 'xml',
  svg: 'xml',
  xsl: 'xml',
  xslt: 'xml',
}

/**
 * Guess the Shiki language identifier from a file path's extension.
 * Returns undefined if the extension is not recognized or the language is not loaded.
 */
export function guessLanguage(filePath: string): string | undefined {
  const ext = extname(filePath)
  return ext ? EXT_TO_LANG[ext] : undefined
}
