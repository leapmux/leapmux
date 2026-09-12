/**
 * What a tool row says when the result carried nothing.
 *
 * It states what LeapMux RECEIVED, never what that implies about the thing the
 * tool acted on. The read body used to say `Empty file` here, which is a claim
 * about the file rather than about the result, and Cursor's refusal to read a
 * 4.8 MB file proved the difference: its ACP `rawOutput.content` is the empty
 * string, so the row told the reader a 4.8 MB file was empty (RL-037).
 *
 * A genuinely empty file reaches this too, and the notice stays true there. One
 * spelling covers the command body, the MCP body and the read body, so a fourth
 * wording cannot appear for the same condition -- the same reason
 * `toolOutcomeLabel` and `TRUNCATION_NOTICE` exist.
 *
 * `[output unavailable]` is a DIFFERENT statement and stays where it is: a
 * referenced stream that could not be recovered is not a stream that was empty.
 */
export const EMPTY_RESULT_NOTICE = '[no output]'
