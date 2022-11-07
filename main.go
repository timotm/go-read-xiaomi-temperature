package main

import (
	"bytes"
	"encoding/binary"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/influxdata/influxdb-client-go/v2"
	"github.com/muka/go-bluetooth/api"
	"github.com/muka/go-bluetooth/bluez/profile/adapter"
	"github.com/muka/go-bluetooth/bluez/profile/device"
	"github.com/peterbourgon/diskv"
	"github.com/posener/goaction/log"
)

// https://github.com/pvvx/ATC_MiThermometer#reading-measurements-in-advertising-mode

type macAddress [6]uint8
type temperatureAdv struct {
	Address            macAddress
	Temperature        int16
	HumidityPercent    uint16
	BatteryMilliVolt   uint16
	BatteryPercent     uint8
	MeasurementCounter uint8
	Flags              uint8
}

func handleDiscoveries(discovery <-chan *adapter.DeviceDiscovered, ch chan<- temperatureAdv) {
	for ev := range discovery {
		if ev.Type == adapter.DeviceAdded {
			dev, err := device.NewDevice1(ev.Path)
			if err != nil {
				log.Warnf("Can't instantiate %s: %s\n", ev.Path, err)
				continue
			}
			if dev == nil {
				log.Warnf("Can't instantiate %s: not found\n", ev.Path)
				continue
			}

			go func() {
				err = watchDevice(dev, ch)
				if err != nil {
					log.Warnf("Can't watch %s: %s\n", ev.Path, err)
				}
			}()
		}
	}
}

func createTemperatureChannel() (<-chan temperatureAdv, func(), error) {
	ad, err := api.GetDefaultAdapter()

	if err != nil {
		return nil, nil, err
	}

	filter := adapter.DiscoveryFilter{Transport: "le"}
	discoveryCh, cancel, err := api.Discover(ad, &filter)

	if err != nil {
		return nil, nil, err
	}

	temperatureCh := make(chan temperatureAdv)
	go handleDiscoveries(discoveryCh, temperatureCh)

	return temperatureCh, cancel, nil
}

func watchDevice(dev *device.Device1, ch chan<- temperatureAdv) error {
	propChanges, err := dev.WatchProperties()

	if err != nil {
		return err
	}

	for ev := range propChanges {
		if ev.Name == "ServiceData" {
			switch m := ev.Value.(type) {
			case map[string]dbus.Variant:
				for key := range m {
					if !strings.HasPrefix(key, "0000181a") {
						continue
					}

					v := m[key].Value()
					switch vv := v.(type) {
					case []uint8:
						buf := bytes.NewReader(vv)
						temp := temperatureAdv{}
						err = binary.Read(buf, binary.LittleEndian, &temp)
						if err != nil {
							log.Warnf("binary.Read error: %s\n", err)
						} else {
							ch <- temp
						}
					default:
						log.Warnf("Unknown ServiceData value type %T (expecting uint8[])", v)
					}
				}
			default:
				log.Warnf("unknown ServiceData type %T (expecting map[string]dbus.Variant\n", ev.Value)
			}
		}
	}

	return nil
}

func nameFromMac(address macAddress, nameStore *diskv.Diskv) string {

	macString := fmt.Sprintf("%02x-%02x-%02x-%02x-%02x-%02x", address[5], address[4], address[3],
		address[2], address[1], address[0])
	prettyName, err := nameStore.Read(macString)

	if err != nil {
		nameStore.Write(macString, []byte(fmt.Sprintf("Sensor %s", macString)))
		return macString
	}

	return strings.TrimSpace(string(prettyName))
}

func readAndSendTemperatures(ch <-chan temperatureAdv, nameStore *diskv.Diskv, url string, database string, token string) {
	client := influxdb2.NewClientWithOptions(url, token, influxdb2.DefaultOptions().SetBatchSize(20).SetLogLevel(0))

	writeAPI := client.WriteAPI("", database)
	defer writeAPI.Flush()
	defer client.Close()

	for t := range ch {
		name := nameFromMac(t.Address, nameStore)
		fmt.Printf("Got temp %s / %+v\n", name, t)
		p := influxdb2.NewPoint(
			"temperature",
			map[string]string{
				"room": name,
			},
			map[string]interface{}{
				"temperature":        float64(t.Temperature) / 100.0,
				"batteryMv":          int(t.BatteryMilliVolt),
				"humidity":           float64(t.HumidityPercent) / 100.0,
				"batteryPercent":     int(t.BatteryPercent),
				"flags":              int(t.Flags),
				"measurementCounter": int(t.MeasurementCounter),
			},
			time.Now())
		// write asynchronously
		writeAPI.WritePoint(p)
	}
}

func main() {
	nameStore := diskv.New(diskv.Options{
		BasePath:     "/var/lib/temperature/",
		CacheSizeMax: 100 * 1024,
	})

	dbUrl := flag.String("url", "http://localhost:8086", "InfluxDB URL")
	dbToken := flag.String("token", "", "InfluxDB token")
	dbDatabase := flag.String("database", "temperature", "InfluxDB database")
	flag.Parse()

	sigCh := make(chan os.Signal)
	signal.Notify(sigCh, os.Interrupt, os.Kill)

	temperatureCh, btCancel, err := createTemperatureChannel()

	if err != nil {
		log.Fatalf("Can't initialize bt: %s", err)
	}

	defer btCancel()

	go readAndSendTemperatures(temperatureCh, nameStore, *dbUrl, *dbDatabase, *dbToken)

	sig := <-sigCh
	fmt.Printf("Received signal [%v], shutting down\n", sig)
}
