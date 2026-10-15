package cli

import (
	"fmt"
	"os"
	"path/filepath"
)

func acquireStartLock(dataDir string) (func(), error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("create data dir %s: %w", dataDir, err)
	}
	return lockFile(filepath.Join(dataDir, "agtd.start.lock"))
}
