/**
 * Map file extensions to Shiki language identifiers.
 *
 * Values are Shiki bundled-language ids (Shiki ships ~332). They are NOT required
 * to be loaded up front: the token worker (shikiWorker) lazily loads a grammar on
 * first use via `ensureLanguage`, so `guessLanguage` may return an id whose
 * grammar isn't compiled yet. An unrecognized extension returns undefined; an id
 * Shiki doesn't bundle is rejected at load time (the view falls back to plain).
 */

import { extname } from './paths'

// Exported for tests: an exhaustive `resolveBundledLang` check over every value
// guards against id typos (e.g. `csharp` vs `c-sharp`) that would otherwise render
// a recognized extension as plain text with no error.
export const EXT_TO_LANG: Record<string, string> = {
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
  pyw: 'python',
  // Rust
  rs: 'rust',
  // Go
  go: 'go',
  // Java / JVM
  java: 'java',
  kt: 'kotlin',
  kts: 'kotlin',
  scala: 'scala',
  groovy: 'groovy',
  gradle: 'groovy',
  clj: 'clojure',
  cljs: 'clojure',
  cljc: 'clojure',
  // Ruby
  rb: 'ruby',
  rake: 'ruby',
  gemspec: 'ruby',
  erb: 'erb',
  // PHP
  php: 'php',
  // Swift / Apple
  swift: 'swift',
  m: 'objective-c',
  mm: 'objective-cpp',
  applescript: 'applescript',
  scpt: 'applescript',
  // C# / .NET
  cs: 'csharp',
  fs: 'fsharp',
  fsx: 'fsharp',
  fsi: 'fsharp',
  vb: 'vb',
  razor: 'razor',
  // Dart
  dart: 'dart',
  // Shell
  sh: 'bash',
  bash: 'bash',
  zsh: 'bash',
  fish: 'fish',
  ps1: 'powershell',
  psm1: 'powershell',
  psd1: 'powershell',
  bat: 'bat',
  cmd: 'bat',
  // Data / config formats
  json: 'json',
  jsonc: 'json',
  json5: 'json5',
  jsonl: 'jsonl',
  ndjson: 'jsonl',
  jsonnet: 'jsonnet',
  yaml: 'yaml',
  yml: 'yaml',
  toml: 'toml',
  ini: 'ini',
  cfg: 'ini',
  properties: 'properties',
  env: 'dotenv',
  csv: 'csv',
  tsv: 'tsv',
  kdl: 'kdl',
  ron: 'ron',
  cue: 'cue',
  // Web / markup
  html: 'html',
  htm: 'html',
  vue: 'vue',
  svelte: 'svelte',
  astro: 'astro',
  css: 'css',
  scss: 'scss',
  sass: 'sass',
  less: 'less',
  styl: 'stylus',
  stylus: 'stylus',
  pug: 'pug',
  jade: 'jade',
  haml: 'haml',
  twig: 'twig',
  liquid: 'liquid',
  hbs: 'handlebars',
  handlebars: 'handlebars',
  coffee: 'coffee',
  // GraphQL / DB / query
  graphql: 'graphql',
  gql: 'graphql',
  sql: 'sql',
  prisma: 'prisma',
  cypher: 'cypher',
  // Docs / prose
  md: 'markdown',
  markdown: 'markdown',
  mdx: 'mdx',
  rst: 'rst',
  adoc: 'asciidoc',
  asciidoc: 'asciidoc',
  tex: 'latex',
  sty: 'latex',
  bib: 'bibtex',
  wiki: 'wikitext',
  wikitext: 'wikitext',
  // Diff / VCS
  diff: 'diff',
  patch: 'diff',
  // Logs (ANSI escape highlighting)
  log: 'ansi',
  // C / C++
  c: 'c',
  h: 'c',
  cpp: 'cpp',
  cxx: 'cpp',
  cc: 'cpp',
  hpp: 'cpp',
  hxx: 'cpp',
  // Other systems / native
  zig: 'zig',
  nim: 'nim',
  d: 'd',
  v: 'verilog',
  sv: 'system-verilog',
  svh: 'system-verilog',
  vhdl: 'vhdl',
  vhd: 'vhdl',
  asm: 'asm',
  s: 'asm',
  llvm: 'llvm',
  // Functional / academic
  hs: 'haskell',
  elm: 'elm',
  ml: 'ocaml',
  mli: 'ocaml',
  ex: 'elixir',
  exs: 'elixir',
  erl: 'erlang',
  hrl: 'erlang',
  jl: 'julia',
  r: 'r',
  rkt: 'racket',
  scm: 'scheme',
  lisp: 'lisp',
  el: 'elisp',
  purs: 'purescript',
  hx: 'haxe',
  gleam: 'gleam',
  cr: 'crystal',
  // Scripting / data science
  lua: 'lua',
  pl: 'perl',
  pm: 'perl',
  raku: 'raku',
  tcl: 'tcl',
  awk: 'awk',
  matlab: 'matlab',
  gnuplot: 'gnuplot',
  gd: 'gdscript',
  mojo: 'mojo',
  // Infra / IaC / build
  tf: 'terraform',
  tfvars: 'terraform',
  hcl: 'hcl',
  dockerfile: 'docker',
  nix: 'nix',
  cmake: 'cmake',
  make: 'makefile',
  mk: 'makefile',
  nginx: 'nginx',
  pp: 'puppet',
  proto: 'proto',
  sol: 'solidity',
  move: 'move',
  cairo: 'cairo',
  // GPU / shaders
  glsl: 'glsl',
  wgsl: 'wgsl',
  hlsl: 'hlsl',
  // Fortran / legacy
  f90: 'fortran-free-form',
  f95: 'fortran-free-form',
  f03: 'fortran-free-form',
  f: 'fortran-fixed-form',
  for: 'fortran-fixed-form',
  pas: 'pascal',
  cob: 'cobol',
  cbl: 'cobol',
  ada: 'ada',
  adb: 'ada',
  ads: 'ada',
  // Diagrams
  mermaid: 'mermaid',
  mmd: 'mermaid',
  // XML family
  xml: 'xml',
  svg: 'xml',
  xsl: 'xml',
  xslt: 'xml',
}

/**
 * Guess the Shiki language identifier from a file path's extension.
 * Returns undefined when the extension is not recognized.
 */
export function guessLanguage(filePath: string): string | undefined {
  const ext = extname(filePath)
  // `Object.hasOwn`, not a bare `EXT_TO_LANG[ext]`: the record is a plain object, so a
  // file whose extension shadows an `Object.prototype` member (`x.constructor`,
  // `x.toString`) would otherwise return the inherited function -- a non-string
  // "language" that breaks structured-clone when posted to the tokenize worker.
  return ext && Object.hasOwn(EXT_TO_LANG, ext) ? EXT_TO_LANG[ext] : undefined
}
