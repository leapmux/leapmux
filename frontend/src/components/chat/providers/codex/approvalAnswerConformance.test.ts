import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'
import { MCP_ELICITATION_ACTION, MCP_ELICITATION_METHOD } from '~/generated/contracts/mcp-elicitation'
import { isObject, pickObject } from '~/lib/jsonPick'
import { buildElicitationResponse, createElicitationForm, elicitationAcceptMetadata } from '../../controls/elicitationForm'
import { codexElicitation } from './elicitation'
import { codexControls } from './pluginControls'

/**
 * The browser's half of testdata/codex_approval_answer_conformance.json.
 *
 * `TestCodexApprovalAnswerConformance` in the worker replays the same file against
 * the answer that the worker sends by itself in Bypass. The two halves keep one
 * answer for each request. A drift would let Bypass send a shape that the browser's
 * Allow never sends, and Codex could then refuse the answer while the request blocks
 * the turn.
 */
const fixturePath = resolve(dirname(fileURLToPath(import.meta.url)), '../../../../../../testdata/codex_approval_answer_conformance.json')

interface ApprovalAnswerCase {
  name: string
  request: Record<string, unknown> & { id: number | string }
  allowResult?: unknown
  standingResult: unknown
}

const fixture = JSON.parse(readFileSync(fixturePath, 'utf8')) as { cases: ApprovalAnswerCase[] }
const browserCases = fixture.cases.filter(testCase => testCase.allowResult !== undefined)

/** The request id and the JSON-RPC result that reach Codex for one browser answer. */
interface BrowserAnswer {
  id: unknown
  result: unknown
}

/**
 * The answer that the browser sends for Allow, through the same calls that the
 * banner makes: the elicitation control for an elicitation, and the Codex control
 * response for every other request.
 *
 * An elicitation answer travels in the shared control envelope. The worker copies
 * the envelope's inner answer into the JSON-RPC result
 * (`providerkit.ResolveMCPElicitationResponse`), so the inner answer is the result
 * that Codex reads.
 */
function browserAllowAnswer(request: ApprovalAnswerCase['request']): BrowserAnswer {
  const requestId = String(request.id)
  if (request.method === MCP_ELICITATION_METHOD.Codex) {
    const elicitation = codexElicitation(request)
    if (elicitation === undefined)
      throw new Error('the Codex plugin does not read this request as an elicitation')
    const choices: Record<string, string> = {}
    const content = elicitation.mode === 'form' ? createElicitationForm(elicitation.schema).read(choices).content : undefined
    const envelope = buildElicitationResponse(requestId, MCP_ELICITATION_ACTION.Accept, content, elicitationAcceptMetadata(elicitation, choices))
    const inner = pickObject(envelope, 'response')
    return { id: inner?.request_id, result: inner?.response }
  }
  const response = codexControls.buildControlResponse!(request, '', requestId)
  if (!isObject(response))
    throw new Error('the Codex control response is not a JSON-RPC object')
  return { id: response.id, result: response.result }
}

describe('codex approval answer conformance', () => {
  it('loads a corpus with cases for the browser', () => {
    expect(browserCases.length).toBeGreaterThan(0)
  })

  it.each(browserCases)('$name', ({ request, allowResult }) => {
    const before = JSON.stringify(request)
    const answer = browserAllowAnswer(request)
    expect(answer.id).toBe(String(request.id))
    expect(answer.result).toEqual(allowResult)
    expect(JSON.stringify(request)).toBe(before)
  })

  it.each(fixture.cases.filter(testCase => testCase.allowResult !== undefined && testCase.standingResult !== null))(
    'agrees with the standing approval for $name',
    ({ allowResult, standingResult }) => {
      expect(standingResult).toEqual(allowResult)
    },
  )
})
