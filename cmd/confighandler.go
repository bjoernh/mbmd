package cmd

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/volkszaehler/mbmd/meters"
	"github.com/volkszaehler/mbmd/meters/rs485"
	"github.com/volkszaehler/mbmd/meters/sunspec"
)

// Config describes the entire configuration
type Config struct {
	API      string
	Rate     time.Duration
	Mqtt     MqttConfig
	Influx   InfluxConfig
	Adapters []AdapterConfig
	Devices  []DeviceConfig
	Other    map[string]any `mapstructure:",remain"`
}

// MqttConfig describes the mqtt broker configuration
type MqttConfig struct {
	Broker   string
	Topic    string
	User     string
	Password string
	ClientID string
	Qos      int
	Homie    string
}

// InfluxConfig describes the InfluxDB configuration
type InfluxConfig struct {
	URL          string
	Database     string
	Measurement  string
	Organization string
	Token        string
	User         string
	Password     string
}

// AdapterConfig describes device communication parameters
type AdapterConfig struct {
	Device   string
	RTU      bool
	Baudrate int
	Comset   string
}

// DeviceConfig describes a single device's configuration
type DeviceConfig struct {
	Type      string
	ID        uint8
	SubDevice int
	Name      string
	Adapter   string
	// Wiring optionally remaps device phases. Each entry maps an mbmd phase to
	// the source device phase it takes its reading from, e.g. {L1: L2, L2: L1}
	// swaps L1 and L2.
	Wiring map[string]string
}

// DeviceConfigHandler creates map of meter managers from given configuration
type DeviceConfigHandler struct {
	DefaultDevice string
	Managers      map[string]*meters.Manager
}

// NewDeviceConfigHandler creates a configuration handler
func NewDeviceConfigHandler() *DeviceConfigHandler {
	conf := &DeviceConfigHandler{
		Managers: make(map[string]*meters.Manager),
	}
	return conf
}

// createConnection parses adapter string to create TCP or RTU connection
func createConnection(device string, rtu bool, baudrate int, comset string, timeout time.Duration) (res meters.Connection) {
	if device == "mock" {
		res = meters.NewMock(device) // mocked connection
	} else if tcp, _ := regexp.MatchString(":[0-9]+$", device); tcp {
		if rtu {
			// special case: RTU over TCP
			log.Printf("config: creating RTU over TCP connection for %s", device)
			res = meters.NewRTUOverTCP(device) // tcp connection
		} else {
			log.Printf("config: creating TCP connection for %s", device)
			res = meters.NewTCP(device) // tcp connection
			res.Timeout(timeout)
		}
	} else {
		log.Printf("config: creating RTU connection for %s (%dbaud, %s)", device, baudrate, comset)
		if baudrate == 0 || comset == "" {
			log.Fatal("Missing comset configuration. See -h for help.")
		}
		if _, err := os.Stat(device); err != nil {
			log.Fatal(err)
		}
		res = meters.NewRTU(device, baudrate, comset) // serial connection
		res.Timeout(timeout)
	}
	return res
}

// ConnectionManager returns connection manager from cache or creates new connection wrapped by manager
func (conf *DeviceConfigHandler) ConnectionManager(connSpec string, rtu bool, baudrate int, comset string, timeout time.Duration) *meters.Manager {
	manager, ok := conf.Managers[connSpec]
	if !ok {
		conn := createConnection(connSpec, rtu, baudrate, comset, timeout)
		manager = meters.NewManager(conn)
		conf.Managers[connSpec] = manager
	}

	return manager
}

func (conf *DeviceConfigHandler) createDeviceForManager(
	manager *meters.Manager,
	meterType string,
	subdevice int,
) meters.Device {
	var meter meters.Device
	meterType = strings.ToUpper(meterType)

	var isSunspec bool
	sunspecTypes := []string{"FRONIUS", "KOSTAL", "KACO", "SE", "SMA", "SOLAREDGE", "STECA", "SUNS", "SUNSPEC"}
	for _, t := range sunspecTypes {
		if t == meterType {
			isSunspec = true
			break
		}
	}

	sort.SearchStrings(sunspecTypes, meterType)
	if isSunspec {
		meter = sunspec.NewDevice(meterType, subdevice)
	} else {
		if subdevice > 0 {
			log.Fatalf("Invalid subdevice number for device %s: %d", meterType, subdevice)
		}

		var err error
		meter, err = rs485.NewDevice(meterType)
		if err != nil {
			log.Fatalf("Error creating device %s: %v.", meterType, err)
		}
	}

	return meter
}

// CreateDevice creates new device and adds it to the connection manager
func (conf *DeviceConfigHandler) CreateDevice(devConf DeviceConfig) {
	if devConf.Adapter == "" {
		// find default adapter
		if len(conf.Managers) == 1 {
			for a := range conf.Managers {
				log.Printf("config: using default adapter %s for device %v", a, devConf)
				devConf.Adapter = a
			}
		} else {
			log.Fatalf("Missing adapter configuration for device %v", devConf)
		}
	}

	manager, ok := conf.Managers[devConf.Adapter]
	if !ok {
		log.Fatalf("Missing adapter configuration for device %v", devConf)
	}
	meter := conf.createDeviceForManager(manager, devConf.Type, devConf.SubDevice)

	if len(devConf.Wiring) > 0 {
		pm, err := parseWiring(devConf.Wiring)
		if err != nil {
			log.Fatalf("Invalid wiring for device %v: %v.", devConf, err)
		}
		if !pm.IsIdentity() {
			log.Printf("config: device %s remapping phases %v", devConf.Name, devConf.Wiring)
			meter = meters.NewPhaseRemapDevice(meter, pm)
		}
	}

	if err := manager.Add(devConf.ID, meter); err != nil {
		log.Fatalf("Error adding device %v: %v.", devConf, err)
	}
}

// parsePhase parses a phase identifier (L1/L2/L3, case-insensitive, or bare
// 1/2/3) into a phase number 1..3.
func parsePhase(s string) (int, error) {
	p := strings.TrimPrefix(strings.ToUpper(strings.TrimSpace(s)), "L")
	switch p {
	case "1", "2", "3":
		n, _ := strconv.Atoi(p)
		return n, nil
	default:
		return 0, fmt.Errorf("invalid phase %q, expected L1, L2 or L3", s)
	}
}

// parseWiring converts a wiring config map (mbmd phase -> device phase) into a
// meters.PhaseMap. It validates that all phases are L1..L3 and that the device
// phases form a permutation (no device phase feeds two mbmd phases).
func parseWiring(wiring map[string]string) (meters.PhaseMap, error) {
	pm := make(meters.PhaseMap, len(wiring))
	seen := make(map[int]string, len(wiring))
	for k, v := range wiring {
		mbmd, err := parsePhase(k)
		if err != nil {
			return nil, err
		}
		device, err := parsePhase(v)
		if err != nil {
			return nil, err
		}
		if prev, dup := seen[device]; dup {
			return nil, fmt.Errorf("device phase L%d assigned to both %s and L%d", device, prev, mbmd)
		}
		seen[device] = fmt.Sprintf("L%d", mbmd)
		pm[mbmd] = device
	}
	return pm, nil
}

// CreateDeviceFromSpec creates new device from specification string and adds
// it to the connection manager
func (conf *DeviceConfigHandler) CreateDeviceFromSpec(deviceDef string, timeout time.Duration) {
	deviceSplit := strings.Split(deviceDef, "@")
	if len(deviceSplit) == 0 || len(deviceSplit) > 2 {
		log.Fatalf("Cannot parse connect string %s. See -h for help.", deviceDef)
	}

	meterDef := deviceSplit[0]
	connSpec := conf.DefaultDevice
	if len(deviceSplit) == 2 {
		connSpec = deviceSplit[1]
	}

	if connSpec == "" {
		log.Fatalf("Cannot parse connect string- missing physical device or connection for %s. See -h for help.", deviceDef)
	}

	meterSplit := strings.Split(meterDef, ":")
	if len(meterSplit) != 2 {
		log.Fatalf("Cannot parse device definition: %s. See -h for help.", meterDef)
	}

	meterType, devID := meterSplit[0], meterSplit[1]
	if len(strings.TrimSpace(meterType)) == 0 {
		log.Fatalf("Cannot parse device definition- meter type empty: %s. See -h for help.", meterDef)
	}

	var subdevice int
	devIDSplit := strings.SplitN(devID, ".", 2)
	if len(devIDSplit) == 2 {
		var err error
		subdevice, err = strconv.Atoi(devIDSplit[1])
		if err != nil {
			log.Fatalf("Error parsing device id %s: %v. See -h for help.", devID, err)
		}
	} else if len(devIDSplit) > 2 {
		log.Fatalf("Error parsing device id %s. See -h for help.", devID)
	}

	id, err := strconv.Atoi(devIDSplit[0])
	if err != nil {
		log.Fatalf("Error parsing device id %s: %v. See -h for help.", devID, err)
	}

	// If this is an RTU over TCP device, a default RTU over TCP should already
	// have been created of the --rtu flag was specified. We'll not re-check this here.
	manager := conf.ConnectionManager(connSpec, false, 0, "", timeout)

	meter := conf.createDeviceForManager(manager, meterType, subdevice)
	if err := manager.Add(uint8(id), meter); err != nil {
		log.Fatalf("Error adding device %s: %v. See -h for help.", meterDef, err)
	}
}
