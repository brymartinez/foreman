package store

import "os"

// pidSelf is the running process, swapped in tests that need a second holder.
var pidSelf = os.Getpid
