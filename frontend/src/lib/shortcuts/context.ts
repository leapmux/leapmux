import type { ContextValue } from './types'

// ---------------------------------------------------------------------------
// Context key store
// ---------------------------------------------------------------------------

const contextMap = new Map<string, ContextValue>()

/** Lazy context providers — evaluated at dispatch time, not stored. */
const lazyProviders = new Map<string, () => ContextValue>()

export function setContext(key: string, value: ContextValue): void {
  contextMap.set(key, value)
}

export function getContext(key: string): ContextValue {
  const lazy = lazyProviders.get(key)
  if (lazy !== undefined)
    return lazy()
  return contextMap.get(key)
}

export function deleteContext(key: string): void {
  contextMap.delete(key)
}

/**
 * Register a lazy context provider. The function is called at dispatch time
 * to read the current value (e.g. checking `document.activeElement`).
 */
export function registerLazyContext(key: string, provider: () => ContextValue): void {
  lazyProviders.set(key, provider)
}

export function unregisterLazyContext(key: string): void {
  lazyProviders.delete(key)
}

/** Reset all context state — mainly useful for tests. */
export function resetContext(): void {
  contextMap.clear()
  lazyProviders.clear()
}

// ---------------------------------------------------------------------------
// When-expression parser & evaluator
// ---------------------------------------------------------------------------

/**
 * AST node types for when-expressions.
 *
 * Supported syntax:
 *   expr     = or_expr
 *   or_expr  = and_expr ( '||' and_expr )*
 *   and_expr = unary ( '&&' unary )*
 *   unary    = '!' unary | primary
 *   primary  = '(' expr ')' | comparison | identifier
 *   comparison = identifier ('==' | '!=') value
 *   value    = quoted_string | identifier
 */
type WhenNode
  = | { type: 'true' }
    | { type: 'ident', name: string }
    | { type: 'not', child: WhenNode }
    | { type: 'and', left: WhenNode, right: WhenNode }
    | { type: 'or', left: WhenNode, right: WhenNode }
    | { type: 'eq', key: string, value: string }
    | { type: 'neq', key: string, value: string }

const parseCache = new Map<string, WhenNode>()

// Pre-compiled regexes for tokenizer
const IDENT_START_RE = /[a-z_]/i
const IDENT_CHAR_RE = /[\w.\-]/

// Tokenizer
type Token
  = | { type: 'ident', value: string }
    | { type: 'string', value: string }
    | { type: 'op', value: string }
    | { type: 'paren', value: '(' | ')' }

function tokenize(expr: string): Token[] {
  const tokens: Token[] = []
  let i = 0
  while (i < expr.length) {
    const ch = expr[i]
    if (ch === ' ' || ch === '\t') {
      i++
      continue
    }
    if (ch === '(') {
      tokens.push({ type: 'paren', value: '(' })
      i++
      continue
    }
    if (ch === ')') {
      tokens.push({ type: 'paren', value: ')' })
      i++
      continue
    }
    if (ch === '!' && expr[i + 1] !== '=') {
      tokens.push({ type: 'op', value: '!' })
      i++
      continue
    }
    if (ch === '&' && expr[i + 1] === '&') {
      tokens.push({ type: 'op', value: '&&' })
      i += 2
      continue
    }
    if (ch === '|' && expr[i + 1] === '|') {
      tokens.push({ type: 'op', value: '||' })
      i += 2
      continue
    }
    if (ch === '=' && expr[i + 1] === '=') {
      tokens.push({ type: 'op', value: '==' })
      i += 2
      continue
    }
    if (ch === '!' && expr[i + 1] === '=') {
      tokens.push({ type: 'op', value: '!=' })
      i += 2
      continue
    }
    if (ch === '"' || ch === '\'') {
      const quote = ch
      let str = ''
      i++
      while (i < expr.length && expr[i] !== quote) {
        str += expr[i]
        i++
      }
      i++ // skip closing quote
      tokens.push({ type: 'string', value: str })
      continue
    }
    // Identifier: letters, digits, dots, underscores, hyphens
    if (IDENT_START_RE.test(ch)) {
      let ident = ''
      while (i < expr.length && IDENT_CHAR_RE.test(expr[i])) {
        ident += expr[i]
        i++
      }
      // Handle boolean literals
      if (ident === 'true' || ident === 'false') {
        tokens.push({ type: 'ident', value: ident })
      }
      else {
        tokens.push({ type: 'ident', value: ident })
      }
      continue
    }
    // Skip unknown characters
    i++
  }
  return tokens
}

// Recursive descent parser
class Parser {
  private tokens: Token[]
  private pos: number

  constructor(tokens: Token[]) {
    this.tokens = tokens
    this.pos = 0
  }

  private peek(): Token | undefined {
    return this.tokens[this.pos]
  }

  private consume(): Token {
    return this.tokens[this.pos++]
  }

  parse(): WhenNode {
    if (this.tokens.length === 0)
      return { type: 'true' }
    const node = this.parseOr()
    return node
  }

  private parseOr(): WhenNode {
    let left = this.parseAnd()
    while (this.peek()?.type === 'op' && this.peek()?.value === '||') {
      this.consume()
      const right = this.parseAnd()
      left = { type: 'or', left, right }
    }
    return left
  }

  private parseAnd(): WhenNode {
    let left = this.parseUnary()
    while (this.peek()?.type === 'op' && this.peek()?.value === '&&') {
      this.consume()
      const right = this.parseUnary()
      left = { type: 'and', left, right }
    }
    return left
  }

  private parseUnary(): WhenNode {
    if (this.peek()?.type === 'op' && this.peek()?.value === '!') {
      this.consume()
      const child = this.parseUnary()
      return { type: 'not', child }
    }
    return this.parsePrimary()
  }

  private parsePrimary(): WhenNode {
    const tok = this.peek()

    if (tok?.type === 'paren' && tok.value === '(') {
      this.consume()
      const node = this.parseOr()
      // Consume closing paren
      if (this.peek()?.type === 'paren' && this.peek()?.value === ')') {
        this.consume()
      }
      return node
    }

    if (tok?.type === 'ident') {
      const name = this.consume().value

      // Handle boolean literals
      if (name === 'true')
        return { type: 'true' }
      if (name === 'false')
        return { type: 'not', child: { type: 'true' } }

      // Check for comparison operators
      const next = this.peek()
      if (next?.type === 'op' && (next.value === '==' || next.value === '!=')) {
        const op = this.consume().value
        const valTok = this.consume()
        const val = valTok?.value ?? ''
        return op === '=='
          ? { type: 'eq', key: name, value: val }
          : { type: 'neq', key: name, value: val }
      }

      return { type: 'ident', name }
    }

    // Fallback: skip token and return true
    if (tok)
      this.consume()
    return { type: 'true' }
  }
}

function parseWhen(expr: string): WhenNode {
  const trimmed = expr.trim()
  if (!trimmed)
    return { type: 'true' }

  const cached = parseCache.get(trimmed)
  if (cached)
    return cached

  const tokens = tokenize(trimmed)
  const parser = new Parser(tokens)
  const node = parser.parse()
  parseCache.set(trimmed, node)
  return node
}

function evalNode(node: WhenNode, get: (key: string) => ContextValue): boolean {
  switch (node.type) {
    case 'true':
      return true
    case 'ident':
      return !!get(node.name)
    case 'not':
      return !evalNode(node.child, get)
    case 'and':
      return evalNode(node.left, get) && evalNode(node.right, get)
    case 'or':
      return evalNode(node.left, get) || evalNode(node.right, get)
    case 'eq':
      return String(get(node.key) ?? '') === node.value
    case 'neq':
      return String(get(node.key) ?? '') !== node.value
  }
}

/**
 * Evaluate a when-expression against a context getter.
 * Returns true if the expression matches (or if the expression is empty/undefined).
 */
export function evaluateWhen(expr: string | undefined, get?: (key: string) => ContextValue): boolean {
  if (!expr)
    return true
  const node = parseWhen(expr)
  return evalNode(node, get ?? getContext)
}

/** Clear the parse cache — mainly useful for tests. */
export function resetParseCache(): void {
  parseCache.clear()
}
