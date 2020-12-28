package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"log"
	"os"
	"os/signal"

	"github.com/godbus/dbus/v5"
	"github.com/muka/go-bluetooth/api"
	"github.com/muka/go-bluetooth/bluez/profile/adapter"
	"github.com/muka/go-bluetooth/bluez/profile/device"
)

func main() {
	ad, err := api.GetDefaultAdapter()

	if err != nil {
		log.Fatalf("Can't get default adapter: %s", err)
	}

	filter := adapter.DiscoveryFilter{Transport: "le"}
	discovery, cancel, err := api.Discover(ad, &filter)

	if err != nil {
		log.Fatalf("Can't discover: %s", err)
	}

	defer cancel()

	go func() {
		for ev := range discovery {
			if ev.Type == adapter.DeviceAdded {
				dev, err := device.NewDevice1(ev.Path)
				if err != nil {
					fmt.Printf("Can't instantiate %s: %s\n", ev.Path, err)
					continue
				}
				if dev == nil {
					fmt.Printf("Can't instantiate %s: not found\n", ev.Path)
					continue
				}

				go func() {
					err = watchDevice(dev)
					if err != nil {
						fmt.Printf("%s: %s\n", ev.Path, err)
					}
				}()
			}
		}
	}()

	ch := make(chan os.Signal)
	signal.Notify(ch, os.Interrupt, os.Kill)

	sig := <-ch
	fmt.Printf("Received signal [%v], shutting down\n", sig)
}

type temperatureAdv struct {
	MacAddress         [6]uint8
	Temperature        uint16
	HumidityPercent    uint8
	BatteryPercent     uint8
	BatteryMilliVolt   uint16
	FramePacketCounter uint8
}

func watchDevice(dev *device.Device1) error {
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
							fmt.Printf("binary.Read error: %s\n", err)
						} else {
							fmt.Printf("### got temp %+v\n", temp)
						}
					default:
						fmt.Printf("Unknown ServiceData value type %T (expecting uint8[])", v)
					}
				}
			default:
				fmt.Printf("### unknown ServiceData type %T (expecting map[string]dbus.Variant\n", ev.Value)
			}
		}
	}

	return nil
}
