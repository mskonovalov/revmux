//go:build !windows

package archive

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// tryLock takes an exclusive advisory lock on f and never waits for one: ok is false when another open
// file description already holds it.
//
// The lock belongs to the descriptor rather than to the process, so a second Archive on the same round is
// refused even inside one revmux, and the kernel drops it when the holder dies — which is what makes an
// abandoned round distinguishable from a live one with nothing to clean up and nothing to delete.
func tryLock(f *os.File) (ok bool, err error) {
	err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("flock %s: %w", f.Name(), err)
	}
	return true, nil
}
