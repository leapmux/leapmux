import { describe, expect, it } from 'vitest'
import { PLAN_MODE_WITHHELD_TOOLS, planWithheldToolsOffered } from './planToolCatalog'

/** The tool names of the requests that Junie 26.9.22 sent to the mock, in request order. */
const DEFAULT_CATALOG = ['open', 'open_entire_file', 'scroll_down', 'scroll_up', 'ask_user', 'glob_search', 'grep_search', 'bash', 'answer', 'search_replace', 'multi_edit', 'create', 'undo_edit', 'submit', 'spawn_subagent', 'agent_skill_read_doc']
const PLAN_CATALOG_WITHOUT_PLAN = ['open', 'open_entire_file', 'submit', 'ask_user', 'answer', 'glob_search', 'grep_search', 'bash', 'agent_skill_read_doc']
const PLAN_CATALOG_WITH_PLAN = ['open', 'open_entire_file', 'multi_edit', 'submit', 'ask_user', 'answer', 'glob_search', 'grep_search', 'bash', 'agent_skill_read_doc']

describe('planWithheldToolsOffered', () => {
  it('finds no withheld tool in the Plan catalog before a plan exists', () => {
    expect(planWithheldToolsOffered(PLAN_CATALOG_WITHOUT_PLAN)).toEqual([])
  })

  it('finds no withheld tool in the Plan catalog after a plan exists, which adds multi_edit', () => {
    expect(PLAN_CATALOG_WITH_PLAN).toContain('multi_edit')
    expect(planWithheldToolsOffered(PLAN_CATALOG_WITH_PLAN)).toEqual([])
  })

  it('finds every withheld tool in the Default catalog', () => {
    expect(planWithheldToolsOffered(DEFAULT_CATALOG)).toEqual([...PLAN_MODE_WITHHELD_TOOLS])
  })

  it('does not withhold multi_edit, because Plan mode offers it for the plan file', () => {
    expect(PLAN_MODE_WITHHELD_TOOLS).not.toContain('multi_edit')
  })

  it('names only tools that the Default catalog offers', () => {
    for (const tool of PLAN_MODE_WITHHELD_TOOLS)
      expect(DEFAULT_CATALOG).toContain(tool)
  })

  it('reports an empty catalog as free of withheld tools', () => {
    expect(planWithheldToolsOffered([])).toEqual([])
  })
})
