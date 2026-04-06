package retrieve

import "sync"

var (
	wakeMu          sync.RWMutex
	immediateWakeCh chan struct{}
)

// SetImmediateWakeChannel registers the daemon wake channel used to prompt
// retrieval processing outside the normal polling interval.
func SetImmediateWakeChannel(ch chan struct{}) {
	wakeMu.Lock()
	defer wakeMu.Unlock()
	immediateWakeCh = ch
}

// RequestImmediateWake asks the daemon retrieval loop to process queued jobs
// promptly. The send is non-blocking so event processing stays lightweight.
func RequestImmediateWake() {
	wakeMu.RLock()
	ch := immediateWakeCh
	wakeMu.RUnlock()
	if ch == nil {
		return
	}
	select {
	case ch <- struct{}{}:
	default:
	}
}
