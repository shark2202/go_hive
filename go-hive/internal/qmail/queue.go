package qmail

import "sync"

// Queue is the letters still waiting for delivery, in the order they were
// sent. A synced queue is durable: the ids live in mail/queue.jsonl, and each
// letter's text in mail/letters, so a restart can retry what never landed.
type Queue struct {
	store *Store
	mu    sync.Mutex
	ids   []string
}

// NewQueue loads the queue file, if any.
func NewQueue(store *Store) *Queue {
	return &Queue{store: store, ids: store.readQueue()}
}

// Enqueue saves the letter and appends its id to the queue.
func (q *Queue) Enqueue(letter Letter) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if err := q.store.Save(letter); err != nil {
		return err
	}
	for _, id := range q.ids {
		if id == letter.ID {
			return nil
		}
	}
	q.ids = append(q.ids, letter.ID)
	return q.store.writeQueue(q.ids)
}

// Ack removes a letter from the queue once it has been delivered.
func (q *Queue) Ack(id string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	for index, existing := range q.ids {
		if existing != id {
			continue
		}
		q.ids = append(q.ids[:index], q.ids[index+1:]...)
		return q.store.writeQueue(q.ids)
	}
	return nil
}

// IDs lists the letters waiting for delivery.
func (q *Queue) IDs() []string {
	q.mu.Lock()
	defer q.mu.Unlock()
	return append([]string(nil), q.ids...)
}

// Len is how many letters wait.
func (q *Queue) Len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.ids)
}

// Letters reads the queued letters back, skipping ids whose file is gone.
func (q *Queue) Letters() []Letter {
	letters := make([]Letter, 0, q.Len())
	for _, id := range q.IDs() {
		if letter, ok := q.store.Load(id); ok {
			letters = append(letters, letter)
		}
	}
	return letters
}
