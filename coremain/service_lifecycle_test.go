package coremain

import (
	"errors"
	"testing"
)

func TestServerServiceStopIsRepeatable(t *testing.T) {
	done := make(chan struct{})
	close(done)
	want := errors.New("worker stopped")
	ss := &serverService{done: done, runErr: want}
	for i := 0; i < 2; i++ {
		if err := ss.Stop(nil); !errors.Is(err, want) {
			t.Fatalf("Stop #%d error = %v", i+1, err)
		}
	}
}
