package server

import (
	"context"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// sink 把一条 WebSocket 连接包成并发安全的写出口。
//
// WebSocket 不允许并发写，而中继这里天然是多写者：开发机的帧、手机的帧、
// 心跳各来自不同 goroutine。用一把互斥锁串行化是最省事也最不容易出错的做法；
// 帧本身很小，锁竞争不构成瓶颈。
type sink struct {
	conn   *websocket.Conn
	mu     sync.Mutex
	closed bool
}

func newSink(conn *websocket.Conn) *sink { return &sink{conn: conn} }

func (s *sink) Send(frame []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return context.Canceled
	}
	ctx, cancel := contextWithTimeout(writeTimeout)
	defer cancel()
	return s.conn.Write(ctx, websocket.MessageBinary, frame)
}

func (s *sink) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	_ = s.conn.Close(websocket.StatusNormalClosure, "")
}

func contextWithTimeout(d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), d)
}

