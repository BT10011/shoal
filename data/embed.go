// Package data holds reference data compiled into the binary so lookups
// need no files or network at runtime.
package data

import _ "embed"

//go:generate go run ./gen -out oui.csv

// OUI is the IEEE MA-L registry trimmed to "prefix,organisation" rows,
// preceded by a "# ..." line naming the source and fetch date.
//
//go:embed oui.csv
var OUI []byte
