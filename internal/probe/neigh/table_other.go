//go:build !linux && !darwin

package neigh

import "errors"

// TableSource names where the entries come from, for the UI and docs.
const TableSource = "unsupported platform"

// Table is not implemented on this platform.
func Table() ([]Entry, error) {
	return nil, errors.New("reading the neighbour cache is not supported on this platform")
}
