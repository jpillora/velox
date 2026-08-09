package velox

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
)

// benchState is a realistic struct that users would sync.
type benchState struct {
	sync.RWMutex
	State
	Counter  int               `json:"counter"`
	Name     string            `json:"name"`
	Users    map[string]string `json:"users"`
	Messages []string          `json:"messages"`
	Config   map[string]any    `json:"config"`
}

func newBenchState(nUsers, nMessages int) *benchState {
	s := &benchState{
		Name:     "test-server",
		Users:    make(map[string]string, nUsers),
		Messages: make([]string, 0, nMessages),
		Config: map[string]any{
			"debug":    false,
			"maxConns": 100,
			"version":  "1.0.0",
			"features": []string{"sync", "delta", "ws"},
		},
	}
	for i := range nUsers {
		s.Users[fmt.Sprintf("user-%d", i)] = "online"
	}
	for range nMessages {
		s.Messages = append(s.Messages, "message content here that is moderately sized")
	}
	return s
}

const (
	largeStateTargetBytes = 341_817
	largeStateMinBytes    = 330_000
	largeStateMaxBytes    = 355_000
	largeDeltaMinBytes    = 175
	largeDeltaMaxBytes    = 260
)

// largeBenchState is generated entirely in this test file. Its shape mimics a
// lopsided state tree without embedding any production data.
type largeBenchState struct {
	sync.RWMutex
	Machines      map[string]any `json:"machines"`
	Tools         map[string]any `json:"tools"`
	Config        map[string]any `json:"config"`
	Chrome        map[string]any `json:"chrome"`
	Account       map[string]any `json:"account"`
	Alerts        map[string]any `json:"alerts"`
	Auth          map[string]any `json:"auth"`
	Commands      map[string]any `json:"commands"`
	Connections   map[string]any `json:"connections"`
	Features      map[string]any `json:"features"`
	History       map[string]any `json:"history"`
	Layout        map[string]any `json:"layout"`
	Network       map[string]any `json:"network"`
	Notifications map[string]any `json:"notifications"`
	Preferences   map[string]any `json:"preferences"`
	Runtime       map[string]any `json:"runtime"`
	Version       map[string]any `json:"version"`

	mutableLeaf map[string]any
}

type largeBenchFixture struct {
	state     *largeBenchState
	marshal   MarshalFunc
	unchanged []byte
	identical []byte
	changed   []byte
	deltaSize int
	leafA     string
	leafB     string
}

type largeStateShape struct {
	nodes         int
	objects       int
	arrays        int
	strings       int
	stringBytes   int
	numbers       int
	bools         int
	nulls         int
	maxDepth      int
	stringP50     int
	stringP90     int
	maxString     int
	keyP50        int
	maxKey        int
	stringLengths []int
	keyLengths    []int
}

func analyzeLargeState(data []byte) (largeStateShape, error) {
	var root any
	if err := json.Unmarshal(data, &root); err != nil {
		return largeStateShape{}, err
	}

	shape := largeStateShape{}
	var visit func(any, int)
	visit = func(value any, depth int) {
		shape.nodes++
		shape.maxDepth = max(shape.maxDepth, depth)
		switch value := value.(type) {
		case map[string]any:
			shape.objects++
			for key, child := range value {
				shape.keyLengths = append(shape.keyLengths, len(key))
				visit(child, depth+1)
			}
		case []any:
			shape.arrays++
			for _, child := range value {
				visit(child, depth+1)
			}
		case string:
			shape.strings++
			shape.stringBytes += len(value)
			shape.stringLengths = append(shape.stringLengths, len(value))
		case float64:
			shape.numbers++
		case bool:
			shape.bools++
		case nil:
			shape.nulls++
		}
	}
	visit(root, 0)
	shape.stringP50 = percentile(shape.stringLengths, 50)
	shape.stringP90 = percentile(shape.stringLengths, 90)
	shape.maxString = percentile(shape.stringLengths, 100)
	shape.keyP50 = percentile(shape.keyLengths, 50)
	shape.maxKey = percentile(shape.keyLengths, 100)
	return shape, nil
}

func percentile(values []int, percentage int) int {
	if len(values) == 0 {
		return 0
	}
	ordered := append([]int(nil), values...)
	sort.Ints(ordered)
	index := (len(ordered) - 1) * percentage / 100
	return ordered[index]
}

func validateLargeStateShape(tb testing.TB, shape largeStateShape) {
	tb.Helper()
	checks := []struct {
		name          string
		got, min, max int
	}{
		{"nodes", shape.nodes, 11_900, 12_500},
		{"objects", shape.objects, 2_350, 2_650},
		{"arrays", shape.arrays, 420, 480},
		{"strings", shape.strings, 5_500, 6_100},
		{"string bytes", shape.stringBytes, 175_000, 202_000},
		{"numbers", shape.numbers, 2_650, 3_000},
		{"bools", shape.bools, 560, 630},
		{"nulls", shape.nulls, 35, 45},
		{"maximum depth", shape.maxDepth, 9, 11},
		{"string p50", shape.stringP50, 7, 11},
		{"string p90", shape.stringP90, 34, 45},
		{"maximum string length", shape.maxString, 1_800, 2_000},
		{"key p50", shape.keyP50, 7, 9},
		{"maximum key length", shape.maxKey, 60, 64},
	}
	for _, check := range checks {
		if check.got < check.min || check.got > check.max {
			tb.Fatalf("large-state %s is %d, want %d..%d", check.name, check.got, check.min, check.max)
		}
	}
}

func generatedString(prefix string, length int) string {
	if len(prefix) >= length {
		return prefix[:length]
	}
	return prefix + strings.Repeat("x", length-len(prefix))
}

func generatedRecords(prefix string, count int, ordinal *int) []any {
	records := make([]any, count)
	for i := range count {
		n := *ordinal
		*ordinal = n + 1
		details := map[string]any{
			"resource": generatedString(fmt.Sprintf("/synthetic/%s/%04d/", prefix, i), 30+n%11),
		}
		operator := fmt.Sprintf("op-%06d", n)
		if n < 495 {
			details["metadata"] = map[string]any{"operator": operator}
		} else {
			details["operator"] = operator
		}
		if n%2 == 0 {
			details["retries"] = n % 7
		}
		if n%4 == 0 {
			details["healthy"] = n%8 != 0
		}

		record := map[string]any{
			"identity":    fmt.Sprintf("r%07d", n),
			"displayName": generatedString(fmt.Sprintf("name-%07d", n), 18+n%23),
			"sequence":    n,
			"priority":    float64((n*37)%10_000) / 10,
			"attributes":  details,
		}
		if n < 183 {
			record["revision"] = n % 41
		}
		if n < 18 {
			record["visible"] = n%2 == 0
		}
		if n < 7 {
			record["aliases"] = []any{}
		}
		if n%3 == 0 {
			record["enabled"] = n%6 != 0
			record["labels"] = []any{
				fmt.Sprintf("group-%02d", n%23),
				fmt.Sprintf("region-%02d", n%11),
				fmt.Sprintf("class-%02d", n%7),
			}
		}
		if n%12 == 0 {
			record["coordinates"] = []any{n % 1920, n % 1080}
		}
		if n%25 == 0 {
			record["message"] = nil
		} else if n > 154 {
			record["message"] = generatedString(fmt.Sprintf("note-%04d", n), 12+n%29)
		}
		records[i] = record
	}
	return records
}

// padGeneratedMap grows m to approximately targetBytes while keeping every
// generated string below the captured workload's maximum string length.
func padGeneratedMap(m map[string]any, targetBytes int) {
	for i := 0; ; i++ {
		encoded, err := json.Marshal(m)
		if err != nil {
			panic(err)
		}
		gap := targetBytes - len(encoded)
		if gap <= 0 {
			return
		}

		key := fmt.Sprintf("syntheticPadding%02d", i)
		if i == 0 {
			key = generatedString("syntheticPaddingKey", 63)
		}
		m[key] = ""
		empty, err := json.Marshal(m)
		if err != nil {
			panic(err)
		}
		overhead := len(empty) - len(encoded)
		if gap < overhead {
			delete(m, key)
			return
		}
		m[key] = generatedString(fmt.Sprintf("padding-%02d-", i), min(gap-overhead, 1_900))
	}
}

func newLargeBenchFixture(tb testing.TB) *largeBenchFixture {
	tb.Helper()

	ordinal := 0
	projects := map[string]any{"entries": generatedRecords("local-project", 220, &ordinal)}
	toolConfig := map[string]any{"entries": generatedRecords("local-tool", 110, &ordinal)}
	terminals := map[string]any{"entries": generatedRecords("local-terminal", 26, &ordinal)}
	agents := map[string]any{"entries": generatedRecords("local-agent", 18, &ordinal)}
	mcp := map[string]any{"entries": generatedRecords("local-mcp", 10, &ordinal)}
	processTree := map[string]any{"entries": generatedRecords("local-process", 10, &ordinal)}
	capabilities := map[string]any{"entries": generatedRecords("local-capability", 7, &ordinal)}
	padGeneratedMap(projects, 74_500)
	padGeneratedMap(toolConfig, 38_000)
	padGeneratedMap(terminals, 8_800)
	padGeneratedMap(agents, 6_050)
	padGeneratedMap(mcp, 3_550)
	padGeneratedMap(processTree, 3_400)
	padGeneratedMap(capabilities, 2_400)
	local := map[string]any{
		"projects":     projects,
		"toolConfig":   toolConfig,
		"terminals":    terminals,
		"agents":       agents,
		"mcp":          mcp,
		"processTree":  processTree,
		"capabilities": capabilities,
		"services":     generatedRecords("local-service", 24, &ordinal),
	}
	leaf := map[string]any{
		"lastActivity": generatedString("steady-state-a-", 72),
	}
	local["runtime"] = map[string]any{
		"process": map[string]any{
			"supervisor": map[string]any{
				"session": map[string]any{
					"connection": map[string]any{
						"heartbeat": map[string]any{
							"detail": leaf,
						},
					},
				},
			},
		},
	}
	remoteA := map[string]any{
		"projects":    generatedRecords("remote-a-project", 190, &ordinal),
		"toolConfig":  generatedRecords("remote-a-tool", 45, &ordinal),
		"processTree": generatedRecords("remote-a-process", 15, &ordinal),
	}
	remoteB := map[string]any{
		"projects":    generatedRecords("remote-b-project", 105, &ordinal),
		"toolConfig":  generatedRecords("remote-b-tool", 30, &ordinal),
		"processTree": generatedRecords("remote-b-process", 15, &ordinal),
	}
	padGeneratedMap(local, 140_900)
	padGeneratedMap(remoteA, 85_050)
	padGeneratedMap(remoteB, 50_900)

	tools := map[string]any{
		"catalog": generatedRecords("tool-catalog", 105, &ordinal),
	}
	config := map[string]any{
		"profiles": generatedRecords("config-profile", 16, &ordinal),
	}
	chrome := map[string]any{
		"targets": generatedRecords("chrome-target", 14, &ordinal),
	}
	padGeneratedMap(tools, 38_000)
	padGeneratedMap(config, 6_100)
	padGeneratedMap(chrome, 5_600)

	smallTree := func(prefix string, count int) map[string]any {
		return map[string]any{"entries": generatedRecords(prefix, count, &ordinal)}
	}
	account := smallTree("account", 2)
	alerts := smallTree("alert", 2)
	auth := smallTree("auth", 2)
	commands := smallTree("command", 2)
	connections := smallTree("connection", 2)
	features := smallTree("feature", 2)
	history := smallTree("history", 2)
	layout := smallTree("layout", 2)
	network := smallTree("network", 2)
	notifications := smallTree("notification", 2)
	preferences := smallTree("preference", 2)
	runtimeState := smallTree("runtime", 2)
	version := smallTree("version", 2)
	minorTrees := []map[string]any{
		account, alerts, auth, commands, connections, features, history, layout,
		network, notifications, preferences, runtimeState, version,
	}
	minorTreeNames := []string{
		"account", "alerts", "auth", "commands", "connections", "features", "history",
		"layout", "network", "notifications", "preferences", "runtime", "version",
	}
	state := &largeBenchState{
		Machines: map[string]any{
			"local":      local,
			"cJI9lhUjtQ": remoteA,
			"A9jexBGFhT": remoteB,
		},
		Tools:         tools,
		Config:        config,
		Chrome:        chrome,
		Account:       account,
		Alerts:        alerts,
		Auth:          auth,
		Commands:      commands,
		Connections:   connections,
		Features:      features,
		History:       history,
		Layout:        layout,
		Network:       network,
		Notifications: notifications,
		Preferences:   preferences,
		Runtime:       runtimeState,
		Version:       version,
		mutableLeaf:   leaf,
	}
	for i, tree := range minorTrees {
		gap := largeStateTargetBytes - mustMarshalLen(state)
		if gap <= 0 {
			break
		}
		share := gap / (len(minorTrees) - i)
		padGeneratedMap(tree, mustMarshalLen(tree)+share)
	}

	marshal := Marshal(state)
	unchanged, err := marshal()
	if err != nil {
		tb.Fatal(err)
	}
	identical, err := marshal()
	if err != nil {
		tb.Fatal(err)
	}
	if !bytes.Equal(unchanged, identical) {
		tb.Fatal("large-state fixture did not marshal deterministically")
	}
	noChangePatcher := &mergePatcher{}
	if _, err := noChangePatcher.patch(unchanged); err != nil {
		tb.Fatal(err)
	}
	noChangeDelta, err := noChangePatcher.patch(identical)
	if err != nil {
		tb.Fatal(err)
	}
	if !bytes.Equal(noChangeDelta, []byte(`{}`)) {
		tb.Fatalf("large-state byte-identical payload produced delta %s", noChangeDelta)
	}
	if len(unchanged) < largeStateMinBytes || len(unchanged) > largeStateMaxBytes {
		tb.Fatalf("large-state fixture is %d bytes, want %d..%d", len(unchanged), largeStateMinBytes, largeStateMaxBytes)
	}
	shape, err := analyzeLargeState(unchanged)
	if err != nil {
		tb.Fatal(err)
	}
	validateLargeStateShape(tb, shape)
	checkMarshalSize(tb, "machines subtree", state.Machines, 265_000, 290_000)
	checkMarshalSize(tb, "machines.local subtree", local, 135_000, 150_000)
	checkMarshalSize(tb, "machines.cJI9lhUjtQ subtree", remoteA, 78_000, 92_000)
	checkMarshalSize(tb, "machines.A9jexBGFhT subtree", remoteB, 45_000, 58_000)
	checkMarshalSize(tb, "tools subtree", tools, 32_000, 44_000)
	checkMarshalSize(tb, "config subtree", config, 4_500, 8_000)
	checkMarshalSize(tb, "chrome subtree", chrome, 4_000, 7_500)
	checkMarshalSize(tb, "machines.local.projects subtree", projects, 70_000, 79_000)
	checkMarshalSize(tb, "machines.local.toolConfig subtree", toolConfig, 35_000, 41_000)
	checkMarshalSize(tb, "machines.local.terminals subtree", terminals, 8_000, 10_000)
	checkMarshalSize(tb, "machines.local.agents subtree", agents, 5_400, 6_800)
	checkMarshalSize(tb, "machines.local.mcp subtree", mcp, 3_100, 4_000)
	checkMarshalSize(tb, "machines.local.processTree subtree", processTree, 3_000, 3_900)
	checkMarshalSize(tb, "machines.local.capabilities subtree", capabilities, 2_100, 2_800)
	minorMax := 0
	for i, tree := range minorTrees {
		size := mustMarshalLen(tree)
		minorMax = max(minorMax, size)
		if size >= 2_500 {
			tb.Fatalf("large-state minor %s subtree is %d bytes, want less than 2500", minorTreeNames[i], size)
		}
	}
	leafA := leaf["lastActivity"].(string)
	leafB := generatedString("steady-state-b-", len(leafA))
	leaf["lastActivity"] = leafB
	changed, err := marshal()
	if err != nil {
		tb.Fatal(err)
	}
	leaf["lastActivity"] = leafA
	if bytes.Equal(unchanged, changed) {
		tb.Fatal("large-state changed payload is byte-identical")
	}

	patcher := &mergePatcher{}
	if _, err := patcher.patch(unchanged); err != nil {
		tb.Fatal(err)
	}
	delta, err := patcher.patch(changed)
	if err != nil {
		tb.Fatal(err)
	}
	if len(delta) < largeDeltaMinBytes || len(delta) > largeDeltaMaxBytes {
		tb.Fatalf("large-state delta is %d bytes, want %d..%d", len(delta), largeDeltaMinBytes, largeDeltaMaxBytes)
	}
	reverseDelta, err := patcher.patch(unchanged)
	if err != nil {
		tb.Fatal(err)
	}
	if len(reverseDelta) < largeDeltaMinBytes || len(reverseDelta) > largeDeltaMaxBytes {
		tb.Fatalf("large-state reverse delta is %d bytes, want %d..%d", len(reverseDelta), largeDeltaMinBytes, largeDeltaMaxBytes)
	}

	return &largeBenchFixture{
		state:     state,
		marshal:   marshal,
		unchanged: unchanged,
		identical: identical,
		changed:   changed,
		deltaSize: len(delta),
		leafA:     leafA,
		leafB:     leafB,
	}
}

func mustMarshalLen(v any) int {
	encoded, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return len(encoded)
}

func checkMarshalSize(tb testing.TB, name string, v any, minBytes, maxBytes int) {
	tb.Helper()
	size := mustMarshalLen(v)
	if size < minBytes || size > maxBytes {
		tb.Fatalf("large-state %s is %d bytes, want %d..%d", name, size, minBytes, maxBytes)
	}
}

func (f *largeBenchFixture) setLeaf(value string) {
	f.state.Lock()
	f.state.mutableLeaf["lastActivity"] = value
	f.state.Unlock()
}

// -------------------------------------------------------------------
// Individual stages
// -------------------------------------------------------------------

// BenchmarkMarshal measures json.Marshal allocations on the user struct.
func BenchmarkMarshal(b *testing.B) {
	for _, size := range []struct {
		name              string
		nUsers, nMessages int
	}{
		{"small", 5, 10},
		{"medium", 50, 100},
		{"large", 500, 1000},
	} {
		s := newBenchState(size.nUsers, size.nMessages)
		b.Run(size.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				data, err := json.Marshal(s)
				if err != nil {
					b.Fatal(err)
				}
				_ = data
			}
		})
	}
}

// BenchmarkCreateMergePatch measures the cached merge patcher.
func BenchmarkCreateMergePatch(b *testing.B) {
	for _, size := range []struct {
		name              string
		nUsers, nMessages int
	}{
		{"small", 5, 10},
		{"medium", 50, 100},
		{"large", 500, 1000},
	} {
		s := newBenchState(size.nUsers, size.nMessages)
		oldBytes, _ := json.Marshal(s)
		s.Counter = 42
		s.Users["user-0"] = "offline"
		newBytes, _ := json.Marshal(s)

		mp := &mergePatcher{}
		mp.patch(oldBytes) // seed cache

		b.Run(size.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				delta, err := mp.patch(newBytes)
				if err != nil {
					b.Fatal(err)
				}
				_ = delta
			}
		})
	}
}

// BenchmarkUpdateMarshal measures json.Marshal of the Update envelope.
func BenchmarkUpdateMarshal(b *testing.B) {
	body, _ := json.Marshal(newBenchState(50, 100))
	upd := &Update{Version: 42, Body: body}

	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		data, err := json.Marshal(upd)
		if err != nil {
			b.Fatal(err)
		}
		_ = data
	}
}

// -------------------------------------------------------------------
// Full push cycle
// -------------------------------------------------------------------

// BenchmarkFullPushCycle simulates the complete gopush hot path.
func BenchmarkFullPushCycle(b *testing.B) {
	s := newBenchState(50, 100)
	initBytes, _ := json.Marshal(s)

	mp := &mergePatcher{}
	mp.patch(initBytes)

	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		s.Counter++
		newBytes, _ := json.Marshal(s)
		delta, _ := mp.patch(newBytes)
		upd := &Update{Version: int64(s.Counter), Delta: true, Body: delta}
		out, _ := json.Marshal(upd)
		_ = out
	}
}

// BenchmarkLargeStateMarshal measures the lock and panic-safe wrapper used by
// State.gopush, in addition to encoding/json itself.
func BenchmarkLargeStateMarshal(b *testing.B) {
	fixture := newLargeBenchFixture(b)
	b.ReportAllocs()
	b.SetBytes(int64(len(fixture.unchanged)))
	b.ResetTimer()

	var out json.RawMessage
	for b.Loop() {
		var err error
		out, err = fixture.marshal()
		if err != nil {
			b.Fatal(err)
		}
	}
	benchmarkBytes = out
	b.ReportMetric(float64(len(fixture.unchanged)), "state_B/op")
}

// BenchmarkLargeStateMergePatch isolates the unmarshal and objectDiff stages.
func BenchmarkLargeStateMergePatch(b *testing.B) {
	fixture := newLargeBenchFixture(b)

	b.Run("no-change", func(b *testing.B) {
		patcher := &mergePatcher{}
		if _, err := patcher.patch(fixture.unchanged); err != nil {
			b.Fatal(err)
		}

		b.ReportAllocs()
		b.SetBytes(int64(len(fixture.unchanged)))
		b.ResetTimer()
		var delta []byte
		for b.Loop() {
			var err error
			delta, err = patcher.patch(fixture.identical)
			if err != nil {
				b.Fatal(err)
			}
		}
		benchmarkBytes = delta
		b.ReportMetric(float64(len(delta)), "delta_B/op")
		b.ReportMetric(float64(len(fixture.unchanged)), "state_B/op")
	})

	b.Run("small-delta", func(b *testing.B) {
		patcher := &mergePatcher{}
		if _, err := patcher.patch(fixture.unchanged); err != nil {
			b.Fatal(err)
		}

		b.ReportAllocs()
		b.SetBytes(int64(len(fixture.unchanged)))
		b.ResetTimer()
		changed := true
		var delta []byte
		for b.Loop() {
			payload := fixture.unchanged
			if changed {
				payload = fixture.changed
			}
			var err error
			delta, err = patcher.patch(payload)
			if err != nil {
				b.Fatal(err)
			}
			changed = !changed
		}
		benchmarkBytes = delta
		b.ReportMetric(float64(fixture.deltaSize), "delta_B/op")
		b.ReportMetric(float64(len(fixture.unchanged)), "state_B/op")
	})
}

// BenchmarkLargeStateFullPushCycle combines the same MarshalFunc and patch
// calls made on the hot path. Fixture generation and patcher seeding are not
// timed.
func BenchmarkLargeStateFullPushCycle(b *testing.B) {
	b.Run("no-change", func(b *testing.B) {
		fixture := newLargeBenchFixture(b)
		patcher := &mergePatcher{}
		if _, err := patcher.patch(fixture.unchanged); err != nil {
			b.Fatal(err)
		}
		previousBytes := bytes.Clone(fixture.unchanged)
		delta := []byte(`{}`)

		b.ReportAllocs()
		b.SetBytes(int64(len(fixture.unchanged)))
		b.ResetTimer()
		for b.Loop() {
			stateBytes, err := fixture.marshal()
			if err != nil {
				b.Fatal(err)
			}
			if bytes.Equal(stateBytes, previousBytes) {
				continue
			}
			delta, err = patcher.patch(stateBytes)
			if err != nil {
				b.Fatal(err)
			}
			previousBytes = bytes.Clone(stateBytes)
		}
		benchmarkBytes = delta
		b.ReportMetric(float64(len(delta)), "delta_B/op")
		b.ReportMetric(float64(len(fixture.unchanged)), "state_B/op")
	})

	b.Run("small-delta", func(b *testing.B) {
		fixture := newLargeBenchFixture(b)
		patcher := &mergePatcher{}
		if _, err := patcher.patch(fixture.unchanged); err != nil {
			b.Fatal(err)
		}
		previousBytes := bytes.Clone(fixture.unchanged)

		b.ReportAllocs()
		b.SetBytes(int64(len(fixture.unchanged)))
		b.ResetTimer()
		changed := true
		var delta []byte
		for b.Loop() {
			b.StopTimer()
			if changed {
				fixture.setLeaf(fixture.leafB)
			} else {
				fixture.setLeaf(fixture.leafA)
			}
			b.StartTimer()
			stateBytes, err := fixture.marshal()
			if err != nil {
				b.Fatal(err)
			}
			if bytes.Equal(stateBytes, previousBytes) {
				continue
			}
			delta, err = patcher.patch(stateBytes)
			if err != nil {
				b.Fatal(err)
			}
			previousBytes = bytes.Clone(stateBytes)
			changed = !changed
		}
		benchmarkBytes = delta
		b.ReportMetric(float64(fixture.deltaSize), "delta_B/op")
		b.ReportMetric(float64(len(fixture.unchanged)), "state_B/op")
	})
}

// BenchmarkLargeStateGopushNoSubscribers measures the synchronous idle
// decision. The large fixture is attached to Data, but the timed loop must not
// marshal or diff it.
func BenchmarkLargeStateGopushNoSubscribers(b *testing.B) {
	fixture := newLargeBenchFixture(b)
	calls := 0
	s := New(func() (json.RawMessage, error) {
		calls++
		return fixture.marshal()
	})
	initialCalls := calls

	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		s.gopush()
	}
	b.StopTimer()
	if calls != initialCalls {
		b.Fatalf("idle gopush called Data %d times", calls-initialCalls)
	}
	b.ReportMetric(float64(len(fixture.unchanged)), "skipped_state_B/op")
}

// -------------------------------------------------------------------
// Pooled buffer encoder
// -------------------------------------------------------------------

var benchmarkBytes []byte

var bufferPool = sync.Pool{
	New: func() any { return new(bytes.Buffer) },
}

func BenchmarkMarshalEncoder(b *testing.B) {
	for _, size := range []struct {
		name              string
		nUsers, nMessages int
	}{
		{"small", 5, 10},
		{"medium", 50, 100},
		{"large", 500, 1000},
	} {
		s := newBenchState(size.nUsers, size.nMessages)
		b.Run(size.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				buf := bufferPool.Get().(*bytes.Buffer)
				buf.Reset()
				if err := json.NewEncoder(buf).Encode(s); err != nil {
					b.Fatal(err)
				}
				_ = buf.Bytes()
				bufferPool.Put(buf)
			}
		})
	}
}

func BenchmarkUpdateMarshalEncoder(b *testing.B) {
	body, _ := json.Marshal(newBenchState(50, 100))
	upd := &Update{Version: 42, Body: body}

	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		buf := bufferPool.Get().(*bytes.Buffer)
		buf.Reset()
		if err := json.NewEncoder(buf).Encode(upd); err != nil {
			b.Fatal(err)
		}
		_ = buf.Bytes()
		bufferPool.Put(buf)
	}
}
