package phx

import "time"

const (
	// defaultConnectTimeout is the default handshake timeout
	defaultConnectTimeout = 10 * time.Second

	// defaultHeartbeatInterval is the default time between heartbeats
	defaultHeartbeatInterval = 30 * time.Second

	// busyWait is the time for goroutines to sleep while waiting. Lower = more CPU. Higher = less responsive
	busyWait = 100 * time.Millisecond

	// messageQueueLength is the number of messages to queue when not connected before blocking
	messageQueueLength = 100
)

func defaultReconnectAfterFunc(tries int) time.Duration {
	schedule := []time.Duration{10, 50, 100, 150, 200, 250, 500, 1000, 2000}
	if tries >= 0 && tries < len(schedule) {
		return schedule[tries] * time.Millisecond
	} else {
		return 5000 * time.Millisecond
	}
}
