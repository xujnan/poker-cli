package history

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
)

// queueSize 是积压上限。真写不动的时候，攒着不如丢掉：牌桌一秒能打好几手，
// 无界队列只会把一次磁盘故障变成一次内存耗尽。
const queueSize = 256

// Writer 把记录追加写进一个文件。
//
// 写盘发生在自己的 goroutine 里，Append 永不阻塞——牌桌是正在发生的事，
// 历史是它的副产物，让一次磁盘错误把几个人的牌局卡死，是拿主要的东西去赔次要的（ADR-0016）。
// 丢掉的和写失败的都计数，Close 的时候报出来，不会静默。
type Writer struct {
	path    string
	f       *os.File
	ch      chan Record
	done    chan struct{}
	wg      sync.WaitGroup
	once    sync.Once
	dropped atomic.Int64
	failed  atomic.Int64
}

// NewWriter 打开（或创建）历史文件并起一个写 goroutine。
func NewWriter(path string) (*Writer, error) {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("history: 无法创建 %s: %w", dir, err)
		}
	}
	// O_APPEND：这个文件只追加，不改也不截断。多个进程同时写同一个文件时，
	// 单次小于管道缓冲的写也因此是原子的，记录不会互相穿插。
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("history: 无法打开 %s: %w", path, err)
	}
	w := &Writer{
		path: path,
		f:    f,
		ch:   make(chan Record, queueSize),
		done: make(chan struct{}),
	}
	w.wg.Add(1)
	go w.loop()
	return w, nil
}

// Path 是这个 Writer 在写的文件。
func (w *Writer) Path() string { return w.path }

// Append 排一条记录去写。队列满了就丢掉并计数，绝不阻塞调用方。
func (w *Writer) Append(r Record) {
	select {
	case <-w.done:
		return
	default:
	}
	select {
	case w.ch <- r:
	default:
		w.dropped.Add(1)
	}
}

// Dropped 是因为积压而丢掉的条数，Failed 是写盘出错的条数。
func (w *Writer) Dropped() int64 { return w.dropped.Load() }
func (w *Writer) Failed() int64  { return w.failed.Load() }

// Close 把队里剩下的写完再关文件。
func (w *Writer) Close() error {
	w.once.Do(func() { close(w.done) })
	w.wg.Wait()
	return w.f.Close()
}

func (w *Writer) loop() {
	defer w.wg.Done()
	for {
		select {
		case r := <-w.ch:
			w.write(r)
		case <-w.done:
			// 把队里排着的写完再走，免得关服务端时丢掉最后几手。
			for {
				select {
				case r := <-w.ch:
					w.write(r)
				default:
					return
				}
			}
		}
	}
}

func (w *Writer) write(r Record) {
	b, err := json.Marshal(r)
	if err != nil {
		w.failed.Add(1)
		return
	}
	if _, err := w.f.Write(append(b, '\n')); err != nil {
		w.failed.Add(1)
	}
}
