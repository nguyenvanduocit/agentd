package proc

import "strconv"

// RotateOp is one step of a log rotation.
type RotateOp string

const (
	RotateRemove   RotateOp = "remove"
	RotateRename   RotateOp = "rename"
	RotateCopy     RotateOp = "copy"
	RotateTruncate RotateOp = "truncate"
)

// RotateStep: remove and truncate act on File; rename and copy go From -> To.
type RotateStep struct {
	Op   RotateOp
	File string
	From string
	To   string
}

// RotateSteps are the steps that rotate file and keep keep files in all (daemon.log, daemon.log.1, daemon.log.2 for
// 3). The active file is copied and truncated in place, not renamed: a service manager that holds it open for the
// daemon's stdout keeps writing to the same name.
func RotateSteps(file string, keep int) []RotateStep {
	older := keep - 1
	if older < 1 {
		return []RotateStep{{Op: RotateTruncate, File: file}}
	}
	steps := []RotateStep{{Op: RotateRemove, File: numbered(file, older)}}
	for i := older - 1; i >= 1; i-- {
		steps = append(steps, RotateStep{Op: RotateRename, From: numbered(file, i), To: numbered(file, i+1)})
	}
	return append(steps,
		RotateStep{Op: RotateCopy, From: file, To: numbered(file, 1)},
		RotateStep{Op: RotateTruncate, File: file},
	)
}

func NeedsRotation(size, maxBytes int64) bool {
	return size > maxBytes
}

func numbered(file string, n int) string {
	return file + "." + strconv.Itoa(n)
}
