// Package mimo runs MiMo Code through its native server: `mimo serve` answers
// REST requests and streams its events as server-sent events. The package does
// not use MiMo's Agent Client Protocol mode, because that mode mixes the output
// of a subagent into the parent's stream with no tag, starts turns that it
// never ends, reports every error as a normal turn end, and loses a steering
// message.
//
// One `mimo serve` process serves one LeapMux agent. The worker starts it with
// a fresh password in its environment, reads the address from the stdout line
// that the server prints once it listens, and then drives one MiMo session over
// HTTP:
//
//   - start.go starts the process and opens the session.
//   - connection.go reads the event stream.
//   - stop.go interrupts a turn, stops the process, and finishes what a stop
//     or an exit left unfinished.
//   - rpc.go holds the REST routes.
//   - events.go decodes an event and dispatches it.
//   - output.go turns message parts into transcript rows and tracks the turn.
//   - control.go handles permissions, questions and interactive commands.
//   - subagent.go gives each MiMo actor a child transcript.
package mimo
