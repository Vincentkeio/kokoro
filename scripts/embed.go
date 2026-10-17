// Package benchtool carries the reviewed test script with the Agent so a
// network test never depends on downloading another executable script first.
package benchtool

import _ "embed"

//go:embed kokoro-bench.sh
var testScript string

// Script returns an independent copy of the script included in this build.
func Script() []byte { return []byte(testScript) }
