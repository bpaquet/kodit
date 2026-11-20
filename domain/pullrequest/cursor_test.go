package pullrequest

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestCursor_FullWhenNeverSynced(t *testing.T) {
	now := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	assert.True(t, NewCursor(1).Full(now, 24*time.Hour))
}

func TestCursor_IncrementalWithinInterval(t *testing.T) {
	now := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	c := ReconstructCursor(1, now.Add(-time.Hour), now.Add(-time.Hour), time.Time{})
	assert.False(t, c.Full(now, 24*time.Hour))
	assert.True(t, c.Full(now, time.Hour))
}

func TestCursor_FullWhilePassInProgress(t *testing.T) {
	now := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	c := ReconstructCursor(1, now, now.Add(-time.Minute), now.Add(-time.Minute))
	assert.True(t, c.Full(now, 24*time.Hour))
}

func TestCursor_StartedKeepsStartOfFullPassInProgress(t *testing.T) {
	first := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	second := first.Add(time.Hour)

	c := NewCursor(1).StartedFull(first).StartedFull(second)

	assert.Equal(t, first, c.FullStartedAt())
}

func TestCursor_CompletedFullPass(t *testing.T) {
	start := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	newest := start.Add(-time.Minute)

	c := NewCursor(1).StartedFull(start).Completed(newest)

	assert.Equal(t, newest, c.Watermark())
	assert.Equal(t, start, c.FullSyncedAt())
	assert.True(t, c.FullStartedAt().IsZero())
}

func TestCursor_CompletedNeverMovesWatermarkBack(t *testing.T) {
	watermark := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	c := ReconstructCursor(1, watermark, watermark, time.Time{})

	assert.Equal(t, watermark, c.Completed(watermark.Add(-time.Hour)).Watermark())
	assert.Equal(t, watermark, c.Completed(time.Time{}).Watermark())
}

func TestState_Indexed(t *testing.T) {
	assert.True(t, StateOpen.Indexed())
	assert.True(t, StateMerged.Indexed())
	assert.False(t, StateClosed.Indexed())
}
