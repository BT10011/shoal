package access

// HasCapability and SetcapPath are for Linux's file capabilities; macOS
// grants raw access by group instead.
func HasCapability(string) bool { return false }
func SetcapPath() string        { return "" }
