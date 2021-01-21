package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"os/signal"
	"strings"

	"github.com/godbus/dbus/v5"
	"github.com/muka/go-bluetooth/api"
	"github.com/muka/go-bluetooth/bluez/profile/adapter"
	"github.com/muka/go-bluetooth/bluez/profile/device"
	"github.com/peterbourgon/diskv"
	"github.com/posener/goaction/log"
)

type macAddress [6]uint8
type temperatureAdv struct {
	Address            macAddress
	Temperature        uint16
	HumidityPercent    uint8
	BatteryPercent     uint8
	BatteryMilliVolt   uint16
	FramePacketCounter uint8
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
					v := m[key].Value()
					switch vv := v.(type) {
					case []uint8:
						temp := temperatureAdv{}
						buf := bytes.NewReader(vv)
						err := binary.Read(buf, binary.BigEndian, &temp)
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

	macString := strings.Trim(strings.Replace(fmt.Sprint(address), " ", "-", -1), "[]")
	prettyName, err := nameStore.Read(macString)

	if err != nil {
		return macString
	}

	return string(prettyName)
}

func readTemperatures(ch <-chan temperatureAdv, nameStore *diskv.Diskv) {
	for t := range ch {
		name := nameFromMac(t.Address, nameStore)
		fmt.Printf("Got temp %s / %+v\n", name, t)
	}
}

func main() {
	nameStore := diskv.New(diskv.Options{
		BasePath:     "/var/lib/temperature/",
		CacheSizeMax: 100 * 1024,
	})

	sigCh := make(chan os.Signal)
	signal.Notify(sigCh, os.Interrupt, os.Kill)

	temperatureCh, btCancel, err := createTemperatureChannel()

	if err != nil {
		log.Fatalf("Can't initialize bt: %s", err)
	}

	defer btCancel()

	go readTemperatures(temperatureCh, nameStore)

	sig := <-sigCh
	fmt.Printf("Received signal [%v], shutting down\n", sig)
}
