package main

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/graydeon/mousa/internal/mousa"
)

// stageFrame is the JSON payload of one served-request stage event. It carries
// metadata only: no request text, record bodies, credentials, or raw errors.
type stageFrame struct {
	RequestID     string                 `json:"request_id"`
	Operation     mousa.RequestOperation `json:"operation"`
	Authenticated bool                   `json:"authenticated"`
	Principal     string                 `json:"principal,omitempty"`
	Client        mousa.ClientMetadata   `json:"client,omitzero"`
	Stage         mousa.Stage            `json:"stage"`
	Phase         mousa.StagePhase       `json:"phase"`
	AtUsec        int64                  `json:"at_usec"`
	DurationUsec  int64                  `json:"duration_usec,omitempty"`
	Attempt       int                    `json:"attempt,omitempty"`
	State         mousa.RequestState     `json:"state,omitempty"`
}

const (
	// stageRingSize bounds the retained live events; older frames drop.
	stageRingSize = 4096
	// stageMaxSubscribers bounds concurrent operator stream viewers.
	stageMaxSubscribers = 8
	// stageWriteBound caps each stream write so a stalled viewer cannot
	// wedge the subscription slot.
	stageWriteBound = 2 * time.Second
	// stageHeartbeat keeps the stream alive through quiet periods.
	stageHeartbeat = 10 * time.Second
)

// stageEvent is one retained stream payload with its sequence.
type stageEvent struct {
	seq  uint64
	data []byte
}

// stageHub is a fixed ring of live stage events shared by all operator
// streams of one service instance. Event ids are epoch:seq; the epoch is a
// fresh random tag per service so a restart cannot be mistaken for a gapless
// continuation. The mutex is held only for ring appends, snapshots, and
// nonblocking subscriber notifications — never for network writes.
type stageHub struct {
	mu    sync.Mutex
	epoch string
	next  uint64
	ring  []stageEvent
	head  int
	count int
	subs  map[chan struct{}]struct{}
}

func newStageHub(size int) *stageHub {
	return &stageHub{
		epoch: rand.Text(),
		next:  1,
		ring:  make([]stageEvent, size),
		subs:  make(map[chan struct{}]struct{}),
	}
}

// publish appends one frame and nudges every subscriber. A frame that cannot
// marshal is dropped: the JSON shape is fixed at compile time.
func (h *stageHub) publish(frame stageFrame) {
	data, err := json.Marshal(frame)
	if err != nil {
		return
	}
	h.mu.Lock()
	event := stageEvent{
		seq:  h.next,
		data: fmt.Appendf(nil, "id: %s:%d\nevent: stage\ndata: %s\n\n", h.epoch, h.next, data),
	}
	h.next++
	h.ring[(h.head+h.count)%len(h.ring)] = event
	if h.count < len(h.ring) {
		h.count++
	} else {
		h.head = (h.head + 1) % len(h.ring)
	}
	for notify := range h.subs {
		select {
		case notify <- struct{}{}:
		default:
		}
	}
	h.mu.Unlock()
}

// since snapshots the retained events after cursor with the oldest retained
// sequence. The ring holds a contiguous run of sequences, so the start index
// is a constant-time offset. A zero oldest means the ring is empty.
func (h *stageHub) since(cursor uint64) (events []stageEvent, oldest uint64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.count == 0 {
		return nil, 0
	}
	oldest = h.ring[h.head].seq
	start := min(uint64(h.count), max(cursor, oldest-1)-(oldest-1))
	events = make([]stageEvent, 0, h.count-int(start))
	for i := int(start); i < h.count; i++ {
		events = append(events, h.ring[(h.head+i)%len(h.ring)])
	}
	return events, oldest
}

// subscribe registers a viewer at the stream tail and reports whether the cap
// allowed it. The returned cursor is the last sequence the viewer is treated
// as having seen.
func (h *stageHub) subscribe() (notify chan struct{}, cursor uint64, ok bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.subs) >= stageMaxSubscribers {
		return nil, 0, false
	}
	notify = make(chan struct{}, 1)
	h.subs[notify] = struct{}{}
	return notify, h.next - 1, true
}

func (h *stageHub) unsubscribe(notify chan struct{}) {
	h.mu.Lock()
	delete(h.subs, notify)
	h.mu.Unlock()
}

// resume interprets a Last-Event-ID cursor: a same-epoch sequence continues
// where it left off, everything else restarts at the tail with an explicit
// gap event so a viewer never silently misses frames.
func (h *stageHub) resume(last string) (cursor uint64, gap []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	tail := h.next - 1
	epoch, raw, found := strings.Cut(last, ":")
	seq, err := strconv.ParseUint(raw, 10, 64)
	if !found || err != nil {
		return tail, h.gapEvent("invalid_cursor", tail)
	}
	if epoch != h.epoch {
		return tail, h.gapEvent("restarted", tail)
	}
	if seq >= h.next {
		return tail, h.gapEvent("future", tail)
	}
	return seq, nil
}

// gapEvent formats one gap marker naming the cursor position it replaces.
// Gap events are advisory: they consume no sequence, so a stream's retained
// run stays contiguous and later cursors never land on phantom ids.
func (h *stageHub) gapEvent(reason string, cursor uint64) []byte {
	return fmt.Appendf(nil, "id: %s:%d\nevent: gap\ndata: {\"reason\":%q,\"epoch\":%q}\n\n",
		h.epoch, cursor, reason, h.epoch)
}

// stageEvents streams live stage frames to operator viewers. It is excluded
// from the request audit loop and holds no in-flight operation slot.
func (s *nativeService) stageEvents(w http.ResponseWriter, r *http.Request) {
	requestID := w.Header().Get("X-Mousa-Request-ID")
	fail := func(status int, code string) {
		_ = s.writeReply(w, status, requestID, nil, &nativeFailure{code: code})
	}
	if !s.operatorOnly(w, r) {
		return
	}
	notify, cursor, ok := s.hub.subscribe()
	if !ok {
		fail(http.StatusServiceUnavailable, "busy")
		return
	}
	defer s.hub.unsubscribe(notify)

	// A resuming viewer presents its last event id; unknown cursors restart
	// at the tail behind an explicit gap marker.
	var gap []byte
	if last := r.Header.Get("Last-Event-ID"); last != "" {
		cursor, gap = s.hub.resume(last)
	}

	w.Header().Set("Content-Type", "text/event-stream")
	controller := http.NewResponseController(w)
	write := func(data []byte) bool {
		_ = controller.SetWriteDeadline(time.Now().Add(stageWriteBound))
		if _, err := w.Write(data); err != nil {
			return false
		}
		return controller.Flush() == nil
	}
	if !write([]byte(": connected\n\n")) {
		return
	}
	if gap != nil && !write(gap) {
		return
	}
	// flush delivers every retained event after the cursor, inserting a gap
	// marker where the fixed ring dropped sequences the viewer missed. The
	// gap marker takes the skipped-to position so the id it announces sorts
	// before every retained frame that follows it.
	flush := func() bool {
		for {
			events, oldest := s.hub.since(cursor)
			if oldest > 0 && cursor+1 < oldest {
				cursor = oldest - 1
				if !write(s.hub.gapEvent("overflow", cursor)) {
					return false
				}
				continue
			}
			for _, event := range events {
				if !write(event.data) {
					return false
				}
				cursor = event.seq
			}
			return true
		}
	}
	if !flush() {
		return
	}
	ticker := time.NewTicker(stageHeartbeat)
	defer ticker.Stop()
	for {
		select {
		case <-notify:
			if !flush() {
				return
			}
		case <-ticker.C:
			if !write([]byte(": heartbeat\n\n")) {
				return
			}
		case <-r.Context().Done():
			return
		}
	}
}
