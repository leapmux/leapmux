import type { MockModelRule } from './mockModelScript'
import { withCleanup } from './cleanup'
import { MOCK_MODEL_IDS } from './mockAgentEnvironment'
import { createMockModelServer } from './mockModelServer'
import { startModelScript } from './modelScriptFixture'

/** One message of an OpenAI chat request. */
export interface ChatMessage {
  role: 'system' | 'user'
  content: string
}

/** The answers that {@link answerHousekeepingBesideContentRule} returns. */
export interface HousekeepingAnswers {
  /** The answer to the housekeeping request. */
  housekeeping: string
  /** The answer to the content turn of the same prompt. */
  content: string
  /** How many requests each rule answered. Several rules can give the same text, so this identifies the rule. */
  ruleMatches: Record<string, number>
}

/** The task of the content prompt. The normal test rule matches it, and the text holds no regular expression syntax. */
const CONTENT_TASK = 'Count slowly to one hundred'

/** The answer of the normal test rule that matches the content prompt. */
export const CONTENT_RULE_ANSWER = 'The content rule answered.'

/**
 * Send a housekeeping request that repeats a prompt, then the content turn of that prompt, and return both answers.
 *
 * A provider can repeat the prompt of its session in a housekeeping request, such as a session title, so a test rule
 * that matches the prompt text matches that request too. The model script holds `rules` as a provider's test object
 * registers them, then a normal rule for the prompt (`content-rule`), as a spec registers it. A caller requires that
 * `rules` answer the housekeeping request and that `content-rule` answer the content turn ({@link CONTENT_RULE_ANSWER}).
 *
 * `housekeepingRequest` builds the messages of the provider's housekeeping request around the marked prompt.
 */
export async function answerHousekeepingBesideContentRule(
  rules: readonly MockModelRule[],
  housekeepingRequest: (prompt: string) => ChatMessage[],
): Promise<HousekeepingAnswers> {
  const server = await createMockModelServer({ models: MOCK_MODEL_IDS })
  return withCleanup(async () => {
    const { script, finish } = await startModelScript(server.url)
    let answered = false
    return withCleanup(async () => {
      // The server refuses an extension that adds nothing.
      if (rules.length > 0)
        await script.rule(...rules)
      await script.rule({ name: 'content-rule', when: { user: CONTENT_TASK }, respond: { text: CONTENT_RULE_ANSWER } })
      const prompt = script.prompt(`${CONTENT_TASK}.`)
      const housekeeping = await chat(server.url, housekeepingRequest(prompt))
      const content = await chat(server.url, [{ role: 'user', content: prompt }])
      const { ruleMatches } = await script.status()
      answered = true
      return { housekeeping, content, ruleMatches }
    }, () => finish(answered))
  }, () => server.close())
}

/** Send one chat request without streaming and return the text of its answer. */
async function chat(serverURL: string, messages: ChatMessage[]): Promise<string> {
  const response = await fetch(`${serverURL}/v1/chat/completions`, {
    method: 'POST',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify({ stream: false, messages }),
  })
  if (response.status !== 200)
    throw new Error(`The mock model server answered ${response.status}: ${await response.text()}`)
  const body = await response.json() as { choices?: Array<{ message?: { content?: unknown } }> }
  const content = body.choices?.[0]?.message?.content
  if (typeof content !== 'string')
    throw new TypeError(`The mock model answer holds no text: ${JSON.stringify(body)}`)
  return content
}
