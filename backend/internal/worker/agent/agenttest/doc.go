// Package agenttest holds the test support that the tests of package agent and
// of each provider share: the fake provider services (Sink, ControlSink, Nop),
// the fixtures, the fake stdin writers, the fake CLI launcher, and the
// conformance suites that each provider runs over itself.
//
// It imports only package agent and neutral utilities, so the test of every
// provider can import it. A test of package agent itself imports it from the
// external test package agent_test, because agenttest imports agent.
package agenttest
