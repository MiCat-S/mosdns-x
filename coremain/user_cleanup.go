package coremain

import (
	"context"
	"time"

	"github.com/pmkol/mosdns-x/internal/control"
	"github.com/pmkol/mosdns-x/internal/telemetry"
	"go.uber.org/zap"
)

// Re-imported IDs cannot serve traffic until their old history is erased:
// minute buckets merge counts and therefore cannot be split with a timestamp.
// This gate also recovers a crash after import committed but cleanup did not.
func drainReusedUserErases(ctx context.Context, queue control.UserEraseQueue, eraser telemetry.BatchedUserEraser, logger *zap.Logger) error {
	count, err := queue.ReusedUserEraseCount(ctx)
	if err != nil || count == 0 {
		return err
	}
	started := time.Now()
	passes := 0
	logger.Info("erasing previous history before enabling re-imported users", zap.Int("users", count))
	for remaining := count; remaining > 0; {
		if err := queue.DrainUserErases(ctx, eraser.DeleteUserBatch); err != nil {
			logger.Warn("startup user history cleanup failed", zap.String("cause", identitySyncCause(err)))
			return err
		}
		passes++
		remaining, err = queue.ReusedUserEraseCount(ctx)
		if err != nil {
			return err
		}
	}
	logger.Info("previous user history erased", zap.Int("users", count), zap.Int("passes", passes), zap.Duration("duration", time.Since(started)))
	return nil
}

func runUserErases(ctx context.Context, queue control.UserEraseQueue, eraser telemetry.BatchedUserEraser, follower *identityFollower, logger *zap.Logger) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		err := queue.DrainUserErases(ctx, eraser.DeleteUserBatch)
		if ctx.Err() != nil {
			return
		}
		if follower != nil {
			follower.mu.Lock()
			follower.cleanupFailed = err != nil
			follower.mu.Unlock()
		}
		if err != nil {
			logger.Warn("deleted user history cleanup failed", zap.String("cause", identitySyncCause(err)))
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
