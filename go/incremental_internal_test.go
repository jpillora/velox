package velox

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// incRecord is deeply immutable: every field copies by value, so a value handed
// out by Get or Range cannot be used to mutate what the container holds.
type incRecord struct {
	Identity string  `json:"identity"`
	Name     string  `json:"displayName"`
	Sequence int     `json:"sequence"`
	Priority float64 `json:"priority"`
	Resource string  `json:"resource"`
}

// countedRecord counts how often it is encoded, which is how the tests observe
// that a clean container really was skipped.
var recordEncodes atomic.Int64

type countedRecord struct {
	Value string `json:"value"`
}

func (r countedRecord) MarshalJSON() ([]byte, error) {
	recordEncodes.Add(1)
	return json.Marshal(struct {
		Value string `json:"value"`
	}{r.Value})
}

type incState struct {
	State
	sync.RWMutex
	Left    VMap[string, countedRecord] `json:"left"`
	Right   VMap[string, countedRecord] `json:"right"`
	Counter int                         `json:"counter"`
}

// quietPusher binds containers without starting the push machinery. A real
// State.Push spawns a worker that marshals in the background, which would both
// pollute the encode counts these tests rely on and make the benchmarks measure
// someone else's marshal.
type quietPusher struct {
	state       *State
	incremental bool
}

func (q *quietPusher) Push() bool                 { return false }
func (q *quietPusher) incrementalEnabled() bool   { return q.incremental }
func (q *quietPusher) registerCache(c *jsonCache) { q.state.registerCache(c) }

func newIncState(t testing.TB, incremental bool) *incState {
	t.Helper()
	s := &incState{}
	s.State.Incremental = incremental
	s.State.Data = Marshal(s)
	bindAll(s, s, &quietPusher{state: &s.State, incremental: incremental})
	for i := range 50 {
		key := fmt.Sprintf("k%02d", i)
		s.Left.Set(key, countedRecord{Value: fmt.Sprintf("left-%02d", i)})
		s.Right.Set(key, countedRecord{Value: fmt.Sprintf("right-%02d", i)})
	}
	return s
}

func TestIncrementalSkipsCleanContainers(t *testing.T) {
	s := newIncState(t, true)
	if _, err := s.State.Data(); err != nil {
		t.Fatal(err)
	}

	// Touch only the left container. The right one is unchanged, so nothing in
	// it should be encoded again.
	s.Left.Set("k00", countedRecord{Value: "changed"})
	recordEncodes.Store(0)
	encoded, err := s.State.Data()
	if err != nil {
		t.Fatal(err)
	}
	encodes := recordEncodes.Load()
	if encodes == 0 {
		t.Fatal("the changed container was not re-encoded")
	}
	if encodes > 50 {
		t.Fatalf("re-encoded %d records, want at most the 50 in the changed container", encodes)
	}
	if !strings.Contains(string(encoded), "changed") {
		t.Fatal("the marshal did not include the change")
	}
	if !strings.Contains(string(encoded), "right-49") {
		t.Fatal("the cached container was dropped from the marshal")
	}

	// A push with nothing touched must encode nothing at all.
	recordEncodes.Store(0)
	again, err := s.State.Data()
	if err != nil {
		t.Fatal(err)
	}
	if got := recordEncodes.Load(); got != 0 {
		t.Fatalf("an unchanged state encoded %d records, want 0", got)
	}
	if string(again) != string(encoded) {
		t.Fatal("the cached marshal differed from the previous one")
	}
}

func TestIncrementalOffAlwaysReEncodes(t *testing.T) {
	s := newIncState(t, false)
	if _, err := s.State.Data(); err != nil {
		t.Fatal(err)
	}
	recordEncodes.Store(0)
	if _, err := s.State.Data(); err != nil {
		t.Fatal(err)
	}
	if got := recordEncodes.Load(); got != 100 {
		t.Fatalf("with caching off %d records were encoded, want all 100", got)
	}
}

// aliasingState uses an element type that a caller can reach through, which is
// exactly the case the cache must refuse.
type aliasingState struct {
	State
	sync.RWMutex
	Items VMap[string, *incRecord] `json:"items"`
}

func TestIncrementalRefusesAliasingElementTypes(t *testing.T) {
	s := &aliasingState{}
	s.State.Incremental = true
	s.State.Data = Marshal(s)
	bindAll(s, s, &quietPusher{state: &s.State, incremental: true})
	s.Items.Set("a", &incRecord{Identity: "before"})

	if s.Items.cache.cacheable {
		t.Fatal("caching engaged for a pointer element type")
	}

	first, err := s.State.Data()
	if err != nil {
		t.Fatal(err)
	}
	// Mutate through a handed-out pointer, which no mutating method observes.
	held, _ := s.Items.Get("a")
	s.Lock()
	held.Identity = "after"
	s.Unlock()

	second, err := s.State.Data()
	if err != nil {
		t.Fatal(err)
	}
	if string(first) == string(second) {
		t.Fatal("a mutation made through an aliased element was not observed")
	}
	if !strings.Contains(string(second), "after") {
		t.Fatalf("marshal = %s, want the aliased mutation", second)
	}
}

func TestVerifyIncrementalCatchesAStaleCache(t *testing.T) {
	s := newIncState(t, true)
	s.State.VerifyIncremental = true
	if _, err := s.State.Data(); err != nil {
		t.Fatal(err)
	}
	cached, err := s.State.Data()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.State.verifyIncremental(cached); err != nil {
		t.Fatalf("a correct cache was reported as diverged: %v", err)
	}

	// Forge exactly the failure the guard exists for: contents that moved on
	// without the cache being told. No supported element type can do this, so it
	// has to be staged directly.
	s.Left.data["k00"] = countedRecord{Value: "mutated behind the cache"}
	s.Left.cache.mut.Lock()
	s.Left.cache.dirty = false
	s.Left.cache.mut.Unlock()

	stale, err := s.State.Data()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.State.verifyIncremental(stale); err == nil {
		t.Fatal("a stale cache was not detected")
	}
}

// batchState covers the hole Batch would otherwise leave: the callback is
// handed the container's own storage, and a caller that keeps that reference
// can mutate the contents with no mutating method called.
type batchState struct {
	State
	sync.RWMutex
	Items VMap[string, countedRecord] `json:"items"`
	List  VSlice[countedRecord]       `json:"list"`
}

func TestIncrementalBatchDoesNotLeakContainerStorage(t *testing.T) {
	s := &batchState{}
	s.State.Incremental = true
	s.State.Data = Marshal(s)
	bindAll(s, s, &quietPusher{state: &s.State, incremental: true})

	var stashedMap map[string]countedRecord
	s.Items.Batch(func(data map[string]countedRecord) {
		data["a"] = countedRecord{Value: "before"}
		stashedMap = data
	})
	var stashedSlice *[]countedRecord
	s.List.Batch(func(data *[]countedRecord) {
		*data = append(*data, countedRecord{Value: "before"})
		stashedSlice = data
	})

	first, err := s.State.Data()
	if err != nil {
		t.Fatal(err)
	}

	// Mutate through the stashed references, exactly as a caller that kept them
	// would. Neither may reach what the container now holds.
	stashedMap["a"] = countedRecord{Value: "smuggled"}
	stashedMap["b"] = countedRecord{Value: "smuggled"}
	*stashedSlice = append(*stashedSlice, countedRecord{Value: "smuggled"})
	(*stashedSlice)[0] = countedRecord{Value: "smuggled"}

	second, err := s.State.Data()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(second), "smuggled") {
		t.Fatalf("a reference kept from Batch reached the container: %s", second)
	}
	if string(first) != string(second) {
		t.Fatalf("state changed without a mutating method:\n first  %s\n second %s", first, second)
	}
	// The cache must still be doing its job afterwards.
	if !s.Items.cache.engaged() || !s.List.cache.engaged() {
		t.Fatal("Batch switched caching off rather than re-homing")
	}
	s.Items.Set("c", countedRecord{Value: "proper"})
	third, err := s.State.Data()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(third), "proper") {
		t.Fatal("a proper mutation after Batch was not observed")
	}
}

func TestDeeplyImmutable(t *testing.T) {
	tests := []struct {
		value any
		want  bool
	}{
		{int(0), true},
		{"", true},
		{float64(0), true},
		{true, true},
		{incRecord{}, true},
		// time.Time carries a *time.Location, so the structural test alone
		// rejects it — and with it most real state structs.
		{time.Time{}, true},
		{time.Duration(0), true},
		{struct{ At time.Time }{}, true},
		{[]time.Time(nil), false},
		{[4]int{}, true},
		{[4]incRecord{}, true},
		{struct{ A struct{ B string } }{}, true},
		{[]int(nil), false},
		{map[string]int(nil), false},
		{(*int)(nil), false},
		{[4]*int{}, false},
		{struct{ A []string }{}, false},
		{struct{ A struct{ B *int } }{}, false},
		{any(nil), false},
	}
	for _, tt := range tests {
		typ := reflect.TypeOf(tt.value)
		got := deeplyImmutable(typ, map[reflect.Type]bool{})
		if got != tt.want {
			t.Errorf("deeplyImmutable(%v) = %v, want %v", typ, got, tt.want)
		}
	}
}

// -------------------------------------------------------------------
// Benchmarks
// -------------------------------------------------------------------

type benchIncState struct {
	State
	sync.RWMutex
	Projects VMap[string, incRecord] `json:"projects"`
	Tools    VMap[string, incRecord] `json:"tools"`
	Agents   VMap[string, incRecord] `json:"agents"`
	Counter  int                     `json:"counter"`
}

func newBenchIncState(tb testing.TB, incremental bool) *benchIncState {
	tb.Helper()
	s := &benchIncState{}
	s.State.Incremental = incremental
	s.State.Data = Marshal(s)
	bindAll(s, s, &quietPusher{state: &s.State, incremental: incremental})
	fill := func(m *VMap[string, incRecord], prefix string, n int) {
		m.Batch(func(data map[string]incRecord) {
			for i := range n {
				key := fmt.Sprintf("%s-%05d", prefix, i)
				data[key] = incRecord{
					Identity: key,
					Name:     generatedString(fmt.Sprintf("name-%05d", i), 18+i%23),
					Sequence: i,
					Priority: float64((i*37)%10_000) / 10,
					Resource: generatedString(fmt.Sprintf("/synthetic/%s/%05d/", prefix, i), 40+i%11),
				}
			}
		})
	}
	fill(&s.Projects, "project", 900)
	fill(&s.Tools, "tool", 450)
	fill(&s.Agents, "agent", 150)
	return s
}

// BenchmarkIncrementalMarshal measures the marshal when one leaf of one
// container changed, which is the shape of a typical push. With caching off the
// whole document is re-encoded; with it on, only the container that moved is.
func BenchmarkIncrementalMarshal(b *testing.B) {
	for _, mode := range []struct {
		name        string
		incremental bool
	}{
		{"off", false},
		{"on", true},
	} {
		b.Run(mode.name, func(b *testing.B) {
			s := newBenchIncState(b, mode.incremental)
			encoded, err := s.State.Data()
			if err != nil {
				b.Fatal(err)
			}
			size := len(encoded)

			b.ReportAllocs()
			b.SetBytes(int64(size))
			b.ResetTimer()
			i := 0
			var out json.RawMessage
			for b.Loop() {
				i++
				s.Agents.Set("agent-00000", incRecord{Identity: "agent-00000", Sequence: i})
				out, err = s.State.Data()
				if err != nil {
					b.Fatal(err)
				}
			}
			benchmarkBytes = out
			b.ReportMetric(float64(size), "state_B/op")
		})
	}
}
