package whatsapp

import (
	"context"
	"errors"
	"sync"
	"time"
)

// pacer queues outbound messages per chat so two consecutive sends to the
// same chat keep a human-like minimum gap. Messages wait in a FIFO queue —
// nothing is dropped for pacing; the only rejections are the queue cap and a
// caller whose context is already done. The queue is in-memory: a restart
// discards pending tasks and the callers see the existing failure paths.
type pacer struct {
	mu      sync.Mutex
	queues  map[string]*chatQueue
	stop    chan struct{}
	stopped bool
}

type chatQueue struct {
	tasks    []*sendTask
	lastSent time.Time
	running  bool
	idle     chan struct{} // closed when a task is appended; re-created when drained
}

// sendTask is one queued send. run performs the actual build-and-send work
// with the caller's context; done carries the result back to the waiter.
type sendTask struct {
	chat string
	ctx  context.Context
	run  func(ctx context.Context) error
	done chan error
}

var (
	// ErrQueueFull means the chat's send queue is at its configured cap.
	ErrQueueFull = errors.New("WhatsApp send queue for this chat is full")
	// errPacerStopped means the channel stopped while the task was queued.
	errPacerStopped = errors.New("WhatsApp channel stopped before the message was sent")
)

const pacerIdleGrace = 2 * time.Second

func newPacer() *pacer {
	return &pacer{queues: map[string]*chatQueue{}, stop: make(chan struct{})}
}

// pacerSend runs run as a paced send to one chat: it waits for the chat's
// turn and the minimum gap, then executes run. The caller's context bounds
// the whole wait; a caller that is already gone is skipped without sending.
func (c *Channel) pacerSend(ctx context.Context, chat string, run func(ctx context.Context) error) error {
	select {
	case <-c.pacer.stop:
		return errPacerStopped
	default:
	}
	task := &sendTask{chat: chat, ctx: ctx, run: run, done: make(chan error, 1)}
	if err := c.pacerOffer(chat, task); err != nil {
		return err
	}
	select {
	case err := <-task.done:
		return err
	case <-ctx.Done():
		// The caller left; the worker still reaps the task at the queue head
		// and skips the send.
		return ctx.Err()
	}
}

// pacerOffer appends one task to a chat's queue and makes sure a worker runs
// it. QueueFull means the chat's queue is at its configured cap.
func (c *Channel) pacerOffer(chat string, task *sendTask) error {
	c.pacer.mu.Lock()
	q, ok := c.pacer.queues[chat]
	if !ok {
		q = &chatQueue{idle: make(chan struct{})}
		c.pacer.queues[chat] = q
	}
	if len(q.tasks) >= c.settingsSnapshot().QueueCap {
		c.pacer.mu.Unlock()
		return ErrQueueFull
	}
	q.tasks = append(q.tasks, task)
	workerMissing := !q.running
	if workerMissing {
		q.running = true
	}
	if q.idle != nil {
		close(q.idle)
		q.idle = nil
	}
	c.pacer.mu.Unlock()
	if workerMissing {
		go c.pacerWorker(chat, q)
	}
	return nil
}

// pacerWorker drains one chat's queue strictly FIFO, honouring the pacing gap.
func (c *Channel) pacerWorker(chat string, q *chatQueue) {
	for {
		select {
		case <-c.pacer.stop:
			c.pacerFailAll(chat, q, errPacerStopped)
			return
		default:
		}
		c.pacer.mu.Lock()
		if len(q.tasks) == 0 {
			idle := q.idle
			if idle == nil {
				idle = make(chan struct{})
				q.idle = idle
			}
			c.pacer.mu.Unlock()
			select {
			case <-idle:
			case <-time.After(pacerIdleGrace):
				c.pacer.mu.Lock()
				if len(q.tasks) == 0 {
					q.running = false
					c.pacer.mu.Unlock()
					return
				}
				c.pacer.mu.Unlock()
			case <-c.pacer.stop:
				c.pacerFailAll(chat, q, errPacerStopped)
				return
			}
			continue
		}
		task := q.tasks[0]
		q.tasks = q.tasks[1:]
		c.pacer.mu.Unlock()

		settings := c.settingsSnapshot()
		gap := time.Duration(settings.SendPacing) * time.Second
		if gap > 0 {
			c.pacer.mu.Lock()
			wait := q.lastSent.Add(gap).Sub(time.Now())
			c.pacer.mu.Unlock()
			if wait > 0 {
				select {
				case <-time.After(wait):
				case <-c.pacer.stop:
					task.done <- errPacerStopped
					c.pacerFailAll(chat, q, errPacerStopped)
					return
				}
			}
		}
		if task.ctx.Err() != nil {
			task.done <- task.ctx.Err()
			continue
		}
		sendCtx, cancel := context.WithTimeout(task.ctx, 20*time.Second)
		err := task.run(sendCtx)
		cancel()
		if err == nil {
			c.pacer.mu.Lock()
			q.lastSent = time.Now()
			c.pacer.mu.Unlock()
		}
		task.done <- err
	}
}

// pacerFailAll completes every queued task for one chat with err.
func (c *Channel) pacerFailAll(chat string, q *chatQueue, err error) {
	c.pacer.mu.Lock()
	tasks := q.tasks
	q.tasks = nil
	c.pacer.mu.Unlock()
	for _, task := range tasks {
		task.done <- err
	}
}

// pacerStop signals every queue on shutdown; queued tasks complete with
// errPacerStopped so the callers' failure notices run.
func (c *Channel) pacerStop() {
	c.pacer.mu.Lock()
	defer c.pacer.mu.Unlock()
	if c.pacer.stopped {
		return
	}
	c.pacer.stopped = true
	close(c.pacer.stop)
	for _, q := range c.pacer.queues {
		for _, task := range q.tasks {
			task.done <- errPacerStopped
		}
		q.tasks = nil
		if q.idle != nil {
			close(q.idle)
			q.idle = nil
		}
	}
}
