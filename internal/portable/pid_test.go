package portable

import "os"

func testPID() int { return os.Getpid() }
