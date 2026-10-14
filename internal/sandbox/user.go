package sandbox

import (
	"fmt"
	"os"
	"runtime"
	"strconv"
)

// sandboxUser is the unprivileged account created in the sandbox image.
// Native Windows cannot supply a host uid: os.Getuid and os.Getgid return -1.
const sandboxUser = "65532:65532"

// dockerUser returns the value passed to docker --user.
// It never returns a negative uid or gid.
func dockerUser(goos string, uid, gid int) (string, error) {
	if goos == "windows" {
		return sandboxUser, nil
	}
	if uid < 0 || gid < 0 {
		return "", fmt.Errorf("docker user: host uid/gid %d:%d is invalid on %s; refusing to pass -1:-1", uid, gid, goos)
	}
	return strconv.Itoa(uid) + ":" + strconv.Itoa(gid), nil
}

func currentDockerUser() (string, error) {
	return dockerUser(runtime.GOOS, os.Getuid(), os.Getgid())
}
