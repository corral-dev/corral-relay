package hub

import (
	"sync"
)

// Destination queues keep host and device receive loops from blocking on a
// slow peer write. Overflow closes only that destination so endpoints can
// resume with a fresh handshake; ciphertext is never dropped mid-stream.
type queuedSink struct {
	inner    Sink
	maxBytes int

	mu       sync.Mutex
	bytes    int
	queue    [][]byte
	closed   bool
	overflow bool
	wake     chan struct{}
	done     chan struct{}
}

func newQueuedSink(inner Sink, maxBytes int) *queuedSink {
	if maxBytes <= 0 {
		maxBytes = queueBytesInteraction
	}
	q := &queuedSink{
		inner:    inner,
		maxBytes: maxBytes,
		queue:    make([][]byte, 0, 8),
		wake:     make(chan struct{}, 1),
		done:     make(chan struct{}),
	}
	go q.loop()
	return q
}

func (q *queuedSink) Send(frame []byte) error {
	q.mu.Lock()
	if q.closed || q.overflow {
		q.mu.Unlock()
		return errDestinationReset
	}
	if q.bytes+len(frame) > q.maxBytes {
		q.overflow = true
		q.closed = true
		q.queue = nil
		q.bytes = 0
		q.mu.Unlock()
		q.signal()
		q.inner.Close()
		return errDestinationReset
	}
	q.queue = append(q.queue, append([]byte(nil), frame...))
	q.bytes += len(frame)
	q.mu.Unlock()
	q.signal()
	return nil
}

func (q *queuedSink) Close() {
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return
	}
	q.closed = true
	q.mu.Unlock()
	q.signal()
	<-q.done
	q.inner.Close()
}

func (q *queuedSink) signal() {
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

func (q *queuedSink) loop() {
	defer close(q.done)
	for {
		frame, ok, done := q.pop()
		if done {
			return
		}
		if !ok {
			<-q.wake
			continue
		}
		if err := q.inner.Send(frame); err != nil {
			q.mu.Lock()
			q.closed = true
			q.queue = nil
			q.bytes = 0
			q.mu.Unlock()
			q.inner.Close()
			return
		}
	}
}

func (q *queuedSink) pop() (frame []byte, ok bool, done bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.overflow {
		return nil, false, true
	}
	if len(q.queue) == 0 {
		if q.closed {
			return nil, false, true
		}
		return nil, false, false
	}
	frame = q.queue[0]
	q.queue[0] = nil
	q.queue = q.queue[1:]
	q.bytes -= len(frame)
	if q.bytes < 0 {
		q.bytes = 0
	}
	return frame, true, false
}

// fairDeviceScheduler records active destinations for fair admission. Writes
// already run on per-destination goroutines so a blocked phone cannot stall
// the host reader; this keeps membership ordered for future credit shaping.
type fairDeviceScheduler struct {
	mu    sync.Mutex
	order [][ChannelIDLen]byte
	next  int
	seen  map[[ChannelIDLen]byte]struct{}
}

func newFairDeviceScheduler() *fairDeviceScheduler {
	return &fairDeviceScheduler{
		seen:  map[[ChannelIDLen]byte]struct{}{},
		order: [][ChannelIDLen]byte{},
	}
}

func (s *fairDeviceScheduler) note(id [ChannelIDLen]byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.seen[id]; ok {
		return
	}
	s.seen[id] = struct{}{}
	s.order = append(s.order, id)
}

func (s *fairDeviceScheduler) remove(id [ChannelIDLen]byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.seen[id]; !ok {
		return
	}
	delete(s.seen, id)
	for i, cur := range s.order {
		if cur != id {
			continue
		}
		s.order = append(s.order[:i], s.order[i+1:]...)
		if s.next > i {
			s.next--
		}
		if len(s.order) > 0 {
			s.next %= len(s.order)
		} else {
			s.next = 0
		}
		return
	}
}
