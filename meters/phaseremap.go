package meters

import (
	"strconv"

	"github.com/grid-x/modbus"
)

// PhaseMap maps an mbmd phase to the source device phase it should take its
// reading from. Phases are 1..3. Phases not present in the map are left
// unchanged (identity). Example: {1: 2, 2: 1} swaps L1 and L2, i.e. mbmd's L1
// reports the device's L2 reading and vice-versa.
type PhaseMap map[int]int

// IsIdentity returns true if the map applies no remapping.
func (pm PhaseMap) IsIdentity() bool {
	for mbmd, device := range pm {
		if mbmd != device {
			return false
		}
	}
	return true
}

// phaseOf returns the phase (1..3) a measurement is associated with and the
// length of its phase suffix. It returns ok=false for measurements without a
// single-phase suffix, including line-to-line voltages such as VoltageL1_L2
// (where the character preceding the trailing phase digit is '_').
func phaseOf(name string) (phase int, ok bool) {
	n := len(name)
	if n < 3 {
		return 0, false
	}
	last := name[n-1]
	if last < '1' || last > '3' {
		return 0, false
	}
	if name[n-2] != 'L' {
		return 0, false
	}
	// exclude line-to-line voltages like VoltageL1_L2
	if n >= 3 && name[n-3] == '_' {
		return 0, false
	}
	return int(last - '0'), true
}

// buildMeasurementRemap builds a translation table from device-tagged
// measurements to mbmd-tagged measurements for the given phase map. Only
// per-phase measurements are remapped; everything else is left untouched.
func buildMeasurementRemap(pm PhaseMap) map[Measurement]Measurement {
	// pm is mbmd -> device; invert to device -> mbmd
	inv := make(map[int]int, len(pm))
	for mbmd, device := range pm {
		inv[device] = mbmd
	}

	remap := make(map[Measurement]Measurement)
	for _, m := range MeasurementValues() {
		name := m.String()
		devicePhase, ok := phaseOf(name)
		if !ok {
			continue
		}
		mbmdPhase, ok := inv[devicePhase]
		if !ok || mbmdPhase == devicePhase {
			continue
		}
		target, err := MeasurementString(name[:len(name)-1] + strconv.Itoa(mbmdPhase))
		if err != nil {
			continue
		}
		remap[m] = target
	}
	return remap
}

// remappingDevice wraps a Device and relabels per-phase measurements according
// to a phase map. Reading values are left untouched, only their phase tag is
// changed.
type remappingDevice struct {
	Device
	remap map[Measurement]Measurement
}

// NewPhaseRemapDevice wraps dev so that its per-phase readings are relabeled
// according to pm. If pm applies no remapping, dev is returned unchanged.
func NewPhaseRemapDevice(dev Device, pm PhaseMap) Device {
	if len(pm) == 0 || pm.IsIdentity() {
		return dev
	}
	remap := buildMeasurementRemap(pm)
	if len(remap) == 0 {
		return dev
	}
	return &remappingDevice{Device: dev, remap: remap}
}

func (d *remappingDevice) apply(r MeasurementResult) MeasurementResult {
	if m, ok := d.remap[r.Measurement]; ok {
		r.Measurement = m
	}
	return r
}

func (d *remappingDevice) Query(client modbus.Client) ([]MeasurementResult, error) {
	res, err := d.Device.Query(client)
	for i := range res {
		res[i] = d.apply(res[i])
	}
	return res, err
}

func (d *remappingDevice) Probe(client modbus.Client) (MeasurementResult, error) {
	res, err := d.Device.Probe(client)
	return d.apply(res), err
}
