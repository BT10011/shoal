//go:build windows

package capture

import (
	"fmt"
	"strings"

	"golang.org/x/sys/windows"
)

// ownerOnlyProblem says why a file is readable by anyone but its owner, or
// returns "" when it is not: an access list protected from inheritance
// whose one entry allows this user.
func ownerOnlyProblem(path string) string {
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err.Error()
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err.Error()
	}
	// In SDDL, e.g. D:PAI(A;;FA;;;S-1-5-21-…): flags before the first
	// entry, P among them for protected (AI, auto-inherited, is Windows'
	// own bookkeeping), then one entry per user allowed in.
	sddl, sid := sd.String(), user.User.Sid.String()
	flags, entries, _ := strings.Cut(strings.TrimPrefix(sddl, "D:"), "(")
	if !strings.HasPrefix(sddl, "D:") || !strings.Contains(flags, "P") || entries != "A;;FA;;;"+sid+")" {
		return fmt.Sprintf("access list %s, want one entry, (A;;FA;;;%s), and the P flag", sddl, sid)
	}
	return ""
}
