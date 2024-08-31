//go:build !linux

package ay

import (
	"runtime"

	"premai.io/Ayup/go/internal/terror"
)

type DaemonStartCmd struct {
}

func (s *DaemonStartCmd) Run(g Globals) (err error) {
	return terror.Errorf(g.Ctx, "Not supported on: %s", runtime.GOOS)
}

type DaemonStartInRootlessCmd struct {
}

func (s *DaemonStartInRootlessCmd) Run(g Globals) (err error) {
	return terror.Errorf(g.Ctx, "Not supported on: %s", runtime.GOOS)
}
