package client

import "os"

// ownedByUs is the Unix ownership check's stand-in. Windows carries no
// Uid in FileInfo, so ownership is not checked there; the mode and the
// regular-file checks still apply.
func ownedByUs(os.FileInfo) bool { return true }
