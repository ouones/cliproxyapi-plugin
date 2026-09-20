//go:build !cgo

package main

// The native host callback implementation is compiled only with cgo. This
// test-only shim keeps the pure registration contract tests runnable on a
// Windows workstation without a C compiler.
func scheduleWireDriftCheck() {}
