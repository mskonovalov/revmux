package archive

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"time"

	"github.com/umputun/revmux/app/task"
)

// dirSize sums the sizes of the regular files under dir, reading sizes rather than block counts so the
// same task measures the same on any filesystem. A symlink contributes nothing and is never followed.
// An entry it cannot read is skipped and named, never fatal and never silently zero — the names come
// back so the caller can say what the total is missing. An absent dir is zero and not even a skip.
func dirSize(dir string) (total int64, unread []string, err error) {
	if _, statErr := os.Lstat(dir); errors.Is(statErr, fs.ErrNotExist) {
		return 0, nil, nil // a task with no rounds yet, or a root no review has written to
	}

	err = filepath.WalkDir(dir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			// an entry that vanished mid-walk is the same case: a concurrent cleanup removing another
			// task, or a run rotating a tee. Both leave the total a floor rather than a wrong number
			unread = append(unread, path)
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		fi, infoErr := d.Info()
		if infoErr != nil {
			unread = append(unread, path)
			return nil //nolint:nilerr // deliberate: the entry is named in unread and the total is a floor
		}
		total += fi.Size()
		return nil
	})
	if err != nil {
		return 0, nil, fmt.Errorf("measure %s: %w", dir, err)
	}
	return total, unread, nil
}

// mb converts bytes to megabytes at one decimal. The consumer is a model deciding what to reclaim, and it
// is comparing tasks rather than accounting for bytes.
func mb(bytes int64) float64 {
	return math.Round(float64(bytes)/(1024*1024)*10) / 10
}

// lastRun is the date of the most recent round's completion, from the finished_at each round's manifest
// carries — when the task was last reviewed rather than when anything last touched the directory. It
// decodes through a local struct rather than importing package main's manifest type, the same way
// events.jsonl is read here; CLAUDE.md's keep-in-sync list carries the cost.
func lastRun(taskDir string, rounds []string) string {
	var newest time.Time
	for _, name := range rounds {
		var rec struct {
			FinishedAt time.Time `json:"finished_at"`
		}
		raw, err := os.ReadFile(filepath.Join(taskDir, name, task.ManifestFile)) //nolint:gosec // a round's own marker
		if err != nil || json.Unmarshal(raw, &rec) != nil {
			continue
		}
		if rec.FinishedAt.After(newest) {
			newest = rec.FinishedAt
		}
	}
	if newest.IsZero() {
		return ""
	}
	return newest.Format(time.DateOnly)
}
