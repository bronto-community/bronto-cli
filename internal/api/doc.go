// Package api holds the hand-written HTTP layer for the Bronto
// management API: the retrying auth Transport, the --debug tracing
// transport, and the status-to-typed-error mapping (ErrorFromStatus).
//
// The vendored OpenAPI spec lives in api/openapi.yaml and is used as a
// conformance/drift reference (resourcespec_test.go, spec-sync's digest),
// NOT for code generation: a generated client shipped here for a while,
// unused — every command speaks through the descriptor registry and
// bronto.Client instead — so it was removed along with its dependency.
package api
