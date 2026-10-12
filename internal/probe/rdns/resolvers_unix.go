//go:build !windows

package rdns

import "os"

// ResolverSource names where the system's DNS servers are read from, for
// messages saying none was found there.
const ResolverSource = ResolvConf

// systemResolvers reads the nameservers in resolv.conf.
func systemResolvers() []Resolver {
	f, err := os.Open(ResolvConf)
	if err != nil {
		return nil
	}
	defer f.Close()
	return ParseResolvConf(f)
}
