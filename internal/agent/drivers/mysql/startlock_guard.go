package mysql

import (
	"context"
	"os"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/proto"
)

var (
	writeConfigHook     func(content string) error
	removeStartLockFile = os.Remove
	clearLock           = (*Pins).ClearLock
)

func releaseStartLock(ctx context.Context, h Host) error { return nil }

func ReleaseStartLock(ctx context.Context, h Host) error { return releaseStartLock(ctx, h) }

func scanBootstrapFailure(tail []byte, file string, since time.Time) (time.Time, bool) {
	return time.Time{}, false
}

func (d *Driver) StartLock() *proto.StartLockState { return nil }
