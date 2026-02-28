package process

import (
	"sync"
	"time"
)

// LogEntry represents a single line of agent output.
type LogEntry struct {
	Timestamp time.Time `json:"timestamp"`
	Stream    string    `json:"stream"` // "stdout" or "stderr"
	Line      string    `json:"line"`
}

// LogBuffer is a thread-safe ring buffer that stores the last N log entries
// and supports real-time streaming to subscribers.
type LogBuffer struct {
	mu          sync.RWMutex
	entries     []LogEntry
	maxEntries  int
	subscribers map[chan LogEntry]struct{}
}

// NewLogBuffer creates a log buffer that retains up to maxEntries lines.
func NewLogBuffer(maxEntries int) *LogBuffer {
	return &LogBuffer{
		entries:     make([]LogEntry, 0, maxEntries),
		maxEntries:  maxEntries,
		subscribers: make(map[chan LogEntry]struct{}),
	}
}

// Write appends a log entry and broadcasts it to all subscribers.
func (lb *LogBuffer) Write(stream, line string) {
	entry := LogEntry{
		Timestamp: time.Now().UTC(),
		Stream:    stream,
		Line:      line,
	}

	lb.mu.Lock()
	if len(lb.entries) >= lb.maxEntries {
		// Drop oldest entry
		lb.entries = lb.entries[1:]
	}
	lb.entries = append(lb.entries, entry)

	// Broadcast to subscribers (non-blocking)
	for ch := range lb.subscribers {
		select {
		case ch <- entry:
		default:
			// subscriber is too slow — drop this entry for them
		}
	}
	lb.mu.Unlock()
}

// Recent returns the last N entries in the buffer.
func (lb *LogBuffer) Recent(n int) []LogEntry {
	lb.mu.RLock()
	defer lb.mu.RUnlock()

	total := len(lb.entries)
	if n <= 0 || n > total {
		n = total
	}
	start := total - n
	result := make([]LogEntry, n)
	copy(result, lb.entries[start:])
	return result
}

// Subscribe returns a channel that receives new log entries as they arrive.
// Call Unsubscribe when done to avoid leaks.
func (lb *LogBuffer) Subscribe() chan LogEntry {
	ch := make(chan LogEntry, 64) // buffer to reduce blocking
	lb.mu.Lock()
	lb.subscribers[ch] = struct{}{}
	lb.mu.Unlock()
	return ch
}

// Unsubscribe removes a subscriber channel and closes it.
func (lb *LogBuffer) Unsubscribe(ch chan LogEntry) {
	lb.mu.Lock()
	delete(lb.subscribers, ch)
	lb.mu.Unlock()
	close(ch)
}
