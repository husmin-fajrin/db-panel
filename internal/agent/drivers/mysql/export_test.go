package mysql

import "io"

// Internals the external tests (package mysql_test) exercise directly.

// PreflightFacts is what mysql.preflight reads from the server and the host.
type PreflightFacts = preflightFacts

// EvaluatePreflight turns facts into checks without a server.
var EvaluatePreflight = evaluatePreflight

// Config rendering and the restart decision, testable without a server.
var (
	RenderConfig   = renderConfig
	RestartReasons = restartReasons
	ParseOptions   = parseOptions
	NotInEffect    = notInEffect
)

// Snapshot helpers, testable without a server.
var (
	DumpArgs       = dumpArgs
	LoadArgs       = loadArgs
	DumpGTIDMode   = dumpGTIDMode
	ClientDefaults = clientDefaults
	FindClientTool = findClientTool
)

// CaptureDumpGTID writes chunks through the dump's counting writer and
// parses the GTID header from what it kept.
func CaptureDumpGTID(chunks []string) (string, bool) {
	c := &countingWriter{w: io.Discard}
	for _, ch := range chunks {
		c.Write([]byte(ch))
	}
	return parseDumpGTID(c.head)
}
