package utils

import (
	"container/heap"
	"context"
	"golang.org/x/sync/semaphore"
	"sync"
	"time"
)

type PrioritySemaphore struct {
	semSize int64
	semCur  int64
	Heap    *PrioSemQueue
	heapMu  sync.Mutex
	propSem *semaphore.Weighted
}

type PrioSemItem struct {
	mu       *semaphore.Weighted
	priority int64
	index    int // The index of the item in the heap.
}
type PrioSemQueue []*PrioSemItem

func (pq PrioSemQueue) Len() int { return len(pq) }

func (pq PrioSemQueue) Less(i, j int) bool {
	// We want Pop to give us the highest, not lowest, priority so we use greater than here.
	return pq[i].priority > pq[j].priority
}

func (pq PrioSemQueue) Swap(i, j int) {
	pq[i], pq[j] = pq[j], pq[i]
	pq[i].index = i
	pq[j].index = j
}

func (pq *PrioSemQueue) Push(x interface{}) {
	n := len(*pq)
	item := x.(*PrioSemItem)
	item.index = n
	*pq = append(*pq, item)
}

func (pq *PrioSemQueue) Pop() interface{} {
	old := *pq
	n := len(old)
	item := old[n-1]
	old[n-1] = nil  // avoid memory leak
	item.index = -1 // for safety
	*pq = old[0 : n-1]
	return item
}

func NewPrioritySem(n int64) *PrioritySemaphore {
	h := make(PrioSemQueue, 0)
	heap.Init(&h)
	return &PrioritySemaphore{
		semSize: n,
		semCur:  n,
		Heap:    &h,
		propSem: semaphore.NewWeighted(1),
	}
}

/*func (ps *PrioritySemaphore) Acquire(con context.Context, prio int64) error {
	ps.heapMu.Lock()
	if ps.semCur > 0 {
		ps.semCur--
		ps.heapMu.Unlock()
		return nil
	}
	semItem := &PrioSemItem{mu: semaphore.NewWeighted(1), priority: prio}
	if err := semItem.mu.Acquire(con, 1); err != nil  {
		ps.heapMu.Unlock()
		return nil
	}
	heap.Push(ps.Heap, semItem)
	ps.heapMu.Unlock()

	if err := semItem.mu.Acquire(con, 1); err != nil {
		return err
	}
	return nil
}*/

func (ps *PrioritySemaphore) Acquire(con context.Context, prio int64) error {
	semItem := &PrioSemItem{mu: semaphore.NewWeighted(1), priority: prio}
	if err := semItem.mu.Acquire(con, 1); err != nil {
		return err
	}
	ps.heapMu.Lock()
	heap.Push(ps.Heap, semItem)
	if ps.semCur > 0 {
		time.AfterFunc(2000*time.Millisecond, func() {
			ps.heapMu.Lock()
			ps.propagate()
			ps.heapMu.Unlock()
		})
	}
	ps.heapMu.Unlock()

	if err := semItem.mu.Acquire(con, 1); err != nil {
		ps.Release()
		return err
	}
	return nil
}

func (ps *PrioritySemaphore) propagate() {
	for ps.semCur > 0 && ps.Heap.Len() > 0 {
		ret := heap.Pop(ps.Heap).(*PrioSemItem)
		ret.mu.Release(1)
		ps.semCur--
	}
}

func (ps *PrioritySemaphore) Release() {
	ps.heapMu.Lock()
	ps.semCur++
	ps.propagate()
	ps.heapMu.Unlock()
}
