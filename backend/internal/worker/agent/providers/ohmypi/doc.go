// Package ohmypi implements the Oh My Pi provider. It drives the omp CLI in its
// `rpc-ui` mode over stdin and stdout.
//
// omp started as a fork of Pi, but its RPC protocol differs from Pi's in its event
// set, its frame shapes, its session files and its tools. This package therefore
// shares no code with package pi. The shared mechanisms both use live in
// providers/internal/providerkit.
package ohmypi
