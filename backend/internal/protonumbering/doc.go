// Package protonumbering holds the test that enforces the protobuf numbering
// rule of AGENTS.md ("Project constraints") over every leapmux message and
// enum: no `reserved`, and no hole in the field or value numbers.
//
// LeapMux is pre-release, so a removed field moves every later field down
// instead of leaving a hole or a reservation. A hole is the trace of an edit
// that skipped that step, and nothing else catches it: protoc accepts any
// numbering.
package protonumbering
