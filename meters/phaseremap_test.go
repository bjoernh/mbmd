package meters

import (
	"testing"

	"github.com/grid-x/modbus"
)

func TestPhaseOf(t *testing.T) {
	cases := []struct {
		name  string
		phase int
		ok    bool
	}{
		{"CurrentL1", 1, true},
		{"PowerL3", 3, true},
		{"ImportL2", 2, true},
		{"FrequencyL1", 1, true},
		{"Current", 0, false},
		{"Power", 0, false},
		{"VoltageL1_L2", 0, false},
		{"VoltageL3_L1", 0, false},
		{"VoltageL_N_avg", 0, false},
	}
	for _, c := range cases {
		phase, ok := phaseOf(c.name)
		if ok != c.ok || phase != c.phase {
			t.Errorf("phaseOf(%q) = (%d, %v), want (%d, %v)", c.name, phase, ok, c.phase, c.ok)
		}
	}
}

func TestBuildMeasurementRemap(t *testing.T) {
	remap := buildMeasurementRemap(PhaseMap{1: 2, 2: 1})

	swapped := []struct{ from, to Measurement }{
		{CurrentL1, CurrentL2},
		{CurrentL2, CurrentL1},
		{PowerL1, PowerL2},
		{PowerL2, PowerL1},
		{ImportL1, ImportL2},
		{ImportL2, ImportL1},
		{VoltageL1, VoltageL2},
		{CosphiL1, CosphiL2},
	}
	for _, s := range swapped {
		if got, ok := remap[s.from]; !ok || got != s.to {
			t.Errorf("remap[%s] = %s (ok=%v), want %s", s.from, got, ok, s.to)
		}
	}

	// untouched measurements must not be in the remap
	for _, m := range []Measurement{CurrentL3, PowerL3, Power, Current, VoltageL1_L2, VoltageL3_L1} {
		if got, ok := remap[m]; ok {
			t.Errorf("remap[%s] = %s, want unchanged", m, got)
		}
	}
}

func TestPhaseMapIsIdentity(t *testing.T) {
	if !(PhaseMap{}).IsIdentity() {
		t.Error("empty map should be identity")
	}
	if !(PhaseMap{1: 1, 2: 2}).IsIdentity() {
		t.Error("self-mapping should be identity")
	}
	if (PhaseMap{1: 2, 2: 1}).IsIdentity() {
		t.Error("swap should not be identity")
	}
}

// fakeDevice is a minimal Device returning fixed measurements.
type fakeDevice struct {
	results []MeasurementResult
}

func (d *fakeDevice) Initialize(client modbus.Client) error { return nil }
func (d *fakeDevice) Descriptor() DeviceDescriptor          { return DeviceDescriptor{} }
func (d *fakeDevice) Probe(client modbus.Client) (MeasurementResult, error) {
	return d.results[0], nil
}
func (d *fakeDevice) Query(client modbus.Client) ([]MeasurementResult, error) {
	return d.results, nil
}

func TestRemappingDeviceQuery(t *testing.T) {
	fake := &fakeDevice{results: []MeasurementResult{
		{Measurement: CurrentL1, Value: 10},
		{Measurement: CurrentL2, Value: 20},
		{Measurement: CurrentL3, Value: 30},
	}}

	dev := NewPhaseRemapDevice(fake, PhaseMap{1: 2, 2: 1})
	res, err := dev.Query(nil)
	if err != nil {
		t.Fatal(err)
	}

	want := map[Measurement]float64{CurrentL1: 20, CurrentL2: 10, CurrentL3: 30}
	for _, r := range res {
		if w, ok := want[r.Measurement]; !ok || w != r.Value {
			t.Errorf("got %s = %v, want %v", r.Measurement, r.Value, w)
		}
	}
}

func TestNewPhaseRemapDeviceIdentity(t *testing.T) {
	fake := &fakeDevice{}
	if dev := NewPhaseRemapDevice(fake, PhaseMap{}); dev != Device(fake) {
		t.Error("empty map should return the original device")
	}
	if dev := NewPhaseRemapDevice(fake, PhaseMap{1: 1, 2: 2}); dev != Device(fake) {
		t.Error("identity map should return the original device")
	}
}
