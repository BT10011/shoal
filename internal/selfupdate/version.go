package selfupdate

import (
	"cmp"
	"strings"
)

// Ahead reports whether current, the running build, is a later version
// than latest, the newest published release: a pre-release built ahead of
// it, such as v2.0.0-rc.1 while v1.0.0 is the latest. Going to that release
// would be a step back, so an update stops instead. It is false when either
// is not a version tag, which leaves the update to go ahead as before.
func Ahead(current, latest string) bool {
	c, ok := parseVersion(current)
	if !ok {
		return false
	}
	l, ok := parseVersion(latest)
	return ok && compareVersions(c, l) > 0
}

// semver is a tag read as semantic versioning spells it:
// vMAJOR.MINOR.PATCH, then an optional -pre-release and +build.
type semver struct {
	core [3]int
	pre  []string
}

func parseVersion(tag string) (semver, bool) {
	var v semver
	s, ok := strings.CutPrefix(tag, "v")
	if !ok {
		return v, false
	}
	s, _, _ = strings.Cut(s, "+") // build metadata plays no part in order
	s, pre, hasPre := strings.Cut(s, "-")
	parts := strings.Split(s, ".")
	if len(parts) != len(v.core) {
		return v, false
	}
	for i, p := range parts {
		n, ok := number(p)
		if !ok {
			return v, false
		}
		v.core[i] = n
	}
	if hasPre {
		v.pre = strings.Split(pre, ".")
		for _, id := range v.pre {
			if id == "" {
				return v, false
			}
		}
	}
	return v, true
}

// compareVersions orders two versions by semantic versioning's rules: the
// numbers first; then a release after its own pre-releases; then the
// pre-releases field by field, numbers by value and before words, which
// sort as text; then the one with more fields last.
func compareVersions(a, b semver) int {
	for i := range a.core {
		if c := cmp.Compare(a.core[i], b.core[i]); c != 0 {
			return c
		}
	}
	if len(a.pre) == 0 || len(b.pre) == 0 {
		return cmp.Compare(len(b.pre), len(a.pre))
	}
	for i := 0; i < len(a.pre) && i < len(b.pre); i++ {
		x, xNum := number(a.pre[i])
		y, yNum := number(b.pre[i])
		var c int
		switch {
		case xNum && yNum:
			c = cmp.Compare(x, y)
		case xNum:
			c = -1
		case yNum:
			c = 1
		default:
			c = strings.Compare(a.pre[i], b.pre[i])
		}
		if c != 0 {
			return c
		}
	}
	return cmp.Compare(len(a.pre), len(b.pre))
}

// number reads a field made only of digits.
func number(s string) (int, bool) {
	if s == "" || len(s) > 9 {
		return 0, false
	}
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, false
		}
		n = n*10 + int(r-'0')
	}
	return n, true
}
