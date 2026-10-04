import type { ToolSpanRole, ToolSpanSide } from './messageSpan'

const requestRole: ToolSpanRole = 'request'
const resultRole: ToolSpanRole = 'result'
const noSideRole: ToolSpanRole = 'none'
const unknownRole: ToolSpanRole = 'other'
const requestSide: ToolSpanSide = requestRole
const resultSide: ToolSpanSide = resultRole

// @ts-expect-error An explicit no-side record cannot identify a pair side.
const noSidePair: ToolSpanSide = noSideRole
// @ts-expect-error An unknown role cannot identify a pair side.
const unknownPair: ToolSpanSide = unknownRole

void [requestSide, resultSide, noSidePair, unknownPair]
