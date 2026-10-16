import ts from 'typescript'

const CHAT_HELPERS = new Set(['userBubbles', 'assistantBubbles', 'messageBubbles', 'messageContents', 'bandRows', 'firstAssistantBubble', 'lastAssistantBubble'])
const CHAT_SELECTORS = ['message-bubble', 'message-content', 'data-seq', 'data-band']
const LOCATOR_METHODS = new Set(['locator', 'first', 'last', 'nth', 'filter', 'and', 'or', 'describe', 'contentFrame', 'elementHandle', 'elementHandles', 'all'])

interface Scope {
  parent?: Scope
  functionScope: boolean
  bindings: Map<string, Binding>
}

interface Binding {
  scope: Scope
  expression?: ts.Expression
  helper?: boolean
}

function bindingNames(name: ts.BindingName): string[] {
  if (ts.isIdentifier(name))
    return [name.text]
  return name.elements.flatMap(element => ts.isBindingElement(element) ? bindingNames(element.name) : [])
}

function member(expression: ts.Expression): { receiver: ts.Expression, name: string } | undefined {
  if (ts.isPropertyAccessExpression(expression))
    return { receiver: expression.expression, name: expression.name.text }
  if (ts.isElementAccessExpression(expression) && ts.isStringLiteral(expression.argumentExpression))
    return { receiver: expression.expression, name: expression.argumentExpression.text }
  return undefined
}

function lookup(name: string, scope: Scope): Binding | undefined {
  for (let current: Scope | undefined = scope; current; current = current.parent) {
    const binding = current.bindings.get(name)
    if (binding)
      return binding
  }
  return undefined
}

/** Find unsafe chat reads without confusing browser DOM nodes or variables from separate scopes. */
export function racyChatReads(source: string, fileName = 'e2e.ts'): Array<{ index: number, text: string }> {
  const file = ts.createSourceFile(fileName, source, ts.ScriptTarget.Latest, true)
  const root: Scope = { functionScope: true, bindings: new Map() }
  const scopes = new WeakMap<ts.Node, Scope>()
  const collect = (node: ts.Node, parent: Scope) => {
    if (ts.isFunctionDeclaration(node) && node.name)
      parent.bindings.set(node.name.text, { scope: parent, helper: CHAT_HELPERS.has(node.name.text) })
    const newScope = ts.isFunctionLike(node) || ts.isBlock(node) || ts.isForStatement(node) || ts.isForOfStatement(node) || ts.isForInStatement(node) || ts.isCatchClause(node)
    const scope: Scope = newScope ? { parent, functionScope: ts.isFunctionLike(node), bindings: new Map() } : parent
    scopes.set(node, scope)
    if (ts.isFunctionLike(node)) {
      for (const parameter of node.parameters) {
        for (const name of bindingNames(parameter.name))
          scope.bindings.set(name, { scope })
      }
    }
    if (ts.isImportSpecifier(node)) {
      const imported = (node.propertyName ?? node.name).text
      scope.bindings.set(node.name.text, { scope, helper: CHAT_HELPERS.has(imported) })
    }
    if (ts.isVariableDeclaration(node)) {
      let owner = scope
      if (ts.isVariableDeclarationList(node.parent) && !(node.parent.flags & ts.NodeFlags.BlockScoped)) {
        while (!owner.functionScope && owner.parent)
          owner = owner.parent
      }
      for (const name of bindingNames(node.name)) {
        owner.bindings.set(name, { scope, ...(ts.isIdentifier(node.name) && node.initializer ? { expression: node.initializer } : {}) })
      }
    }
    ts.forEachChild(node, child => collect(child, scope))
  }
  collect(file, root)

  const selector = (node: ts.Expression, scope: Scope, seen: Set<Binding>): boolean => {
    if (ts.isIdentifier(node)) {
      const binding = lookup(node.text, scope)
      if (!binding?.expression || seen.has(binding))
        return false
      return selector(binding.expression, binding.scope, new Set([...seen, binding]))
    }
    if (ts.isStringLiteralLike(node) || ts.isTemplateExpression(node))
      return CHAT_SELECTORS.some(marker => node.getText(file).includes(marker))
    return false
  }
  const helper = (node: ts.Expression, scope: Scope, seen: Set<Binding>): boolean => {
    if (!ts.isIdentifier(node))
      return false
    const binding = lookup(node.text, scope)
    if (!binding)
      return CHAT_HELPERS.has(node.text)
    if (seen.has(binding))
      return false
    return binding.helper === true || (binding.expression !== undefined && helper(binding.expression, binding.scope, new Set([...seen, binding])))
  }
  const locator = (node: ts.Expression, scope: Scope, seen = new Set<Binding>()): boolean => {
    if (ts.isIdentifier(node)) {
      const binding = lookup(node.text, scope)
      if (!binding?.expression || seen.has(binding))
        return false
      return locator(binding.expression, binding.scope, new Set([...seen, binding]))
    }
    if (ts.isParenthesizedExpression(node) || ts.isAsExpression(node) || ts.isTypeAssertionExpression(node) || ts.isNonNullExpression(node) || ts.isAwaitExpression(node) || ts.isSatisfiesExpression(node))
      return locator(node.expression, scope, seen)
    if (ts.isConditionalExpression(node))
      return locator(node.whenTrue, scope, seen) || locator(node.whenFalse, scope, seen)
    if (ts.isObjectLiteralExpression(node))
      return node.properties.some(property => ts.isPropertyAssignment(property) ? locator(property.initializer, scope, seen) : ts.isShorthandPropertyAssignment(property) && locator(property.name, scope, seen))
    if (ts.isArrayLiteralExpression(node))
      return node.elements.some(element => locator(element, scope, seen))
    if (ts.isElementAccessExpression(node))
      return locator(node.expression, scope, seen)
    if (!ts.isCallExpression(node))
      return false
    if (helper(node.expression, scope, new Set()))
      return true
    const method = member(node.expression)
    if (method) {
      if (['locator', 'getByTestId', 'getByText'].includes(method.name) && node.arguments.some(argument => selector(argument, scope, new Set())))
        return true
      if (!LOCATOR_METHODS.has(method.name) && !method.name.startsWith('getBy'))
        return false
      return locator(method.receiver, scope, seen) || node.arguments.some(argument => locator(argument, scope, seen))
    }
    // A wrapper such as visibleOnly retains the locator that it receives.
    return node.arguments.some(argument => locator(argument, scope, seen))
  }
  const found: Array<{ index: number, text: string }> = []
  const visit = (node: ts.Node) => {
    if (ts.isCallExpression(node)) {
      const method = member(node.expression)
      if (method && ['evaluate', 'evaluateAll'].includes(method.name)) {
        if (locator(method.receiver, scopes.get(node) ?? root))
          found.push({ index: node.getStart(file), text: `${node.expression.getText(file)}(` })
        // These callbacks run in the browser. Their DOM objects are not Playwright locators.
        visit(node.expression)
        for (const argument of node.arguments) {
          if (!ts.isFunctionLike(argument))
            visit(argument)
        }
        return
      }
    }
    ts.forEachChild(node, visit)
  }
  visit(file)
  return found
}
