//go:build windows

package archive

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

// lockOffsetHigh puts the lock on a byte far past anything the manifest will ever hold. Windows
// byte-range locks are mandatory rather than advisory, so locking a byte the record occupies would make
// the run's own manifest un-writable through the handle Writer opens.
const lockOffsetHigh = 1 << 30

// tryLock takes an exclusive lock on f and never waits for one: ok is false when another handle already
// holds it.
//
// The lock belongs to the handle rather than to the process, so a second Archive on the same round is
// refused even inside one revmux, and windows drops it when the holder dies — which is what makes an
// abandoned round distinguishable from a live one with nothing to clean up and nothing to delete.
func tryLock(f *os.File) (ok bool, err error) {
	overlapped := &windows.Overlapped{OffsetHigh: lockOffsetHigh}
	err = windows.LockFileEx(windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, overlapped)
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("lock %s: %w", f.Name(), err)
	}
	return true, nil
}
