package main

import (
	"encoding/json"
	"log"
	"strings"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

type Device struct {
	Identifiers  []string `json:"identifiers"`
	Name         string   `json:"name"`
	Model        string   `json:"model"`
	Manufacturer string   `json:"manufacturer"`
}

// DiscoveryConfig is the Home Assistant MQTT discovery payload for one sensor.
type DiscoveryConfig struct {
	DeviceClass       string `json:"device_class,omitempty"`
	StateClass        string `json:"state_class,omitempty"`
	Name              string `json:"name"`
	StateTopic        string `json:"state_topic"`
	Unit              string `json:"unit_of_measurement,omitempty"`
	ValueTemplate     string `json:"value_template"`
	DefaultEntityID   string `json:"default_entity_id"`
	UniqueID          string `json:"unique_id"`
	AvailabilityTopic string `json:"availability_topic"`
	Device            Device `json:"device"`
	Icon              string `json:"icon,omitempty"`
}

// newDevice matches the device info the Python version sent.
func newDevice(devicename, displayName string) Device {
	osInfo := osRelease()
	manufacturer := osInfo["NAME"]
	if strings.Contains(osInfo["ID"], "rasp") {
		manufacturer = "RPI Foundation"
	}
	return Device{
		Identifiers:  []string{devicename + "_sensor"},
		Name:         displayName + " Sensors",
		Model:        manufacturer + " " + displayName,
		Manufacturer: manufacturer,
	}
}

// publishDiscovery sends a retained config message for every sensor, then marks the device online.
func publishDiscovery(client mqtt.Client, devicename string, device Device, sensors []Sensor) {
	for _, s := range sensors {
		config := DiscoveryConfig{
			DeviceClass:       s.DeviceClass,
			StateClass:        s.StateClass,
			Name:              s.Name,
			StateTopic:        stateTopic(devicename),
			Unit:              s.Unit,
			ValueTemplate:     "{{value_json." + s.Key + "}}",
			DefaultEntityID:   devicename + "_sensor_" + s.Key,
			UniqueID:          devicename + "_sensor_" + s.Key,
			AvailabilityTopic: availabilityTopic(devicename),
			Device:            device,
		}
		if s.Icon != "" {
			config.Icon = "mdi:" + s.Icon
		}
		payload, err := json.Marshal(config)
		if err != nil {
			log.Printf("discovery %s: %v", s.Key, err)
			continue
		}
		client.Publish("homeassistant/sensor/"+devicename+"/"+s.Key+"/config", 1, true, payload)
	}
	client.Publish(availabilityTopic(devicename), 1, true, "online")
}

func stateTopic(devicename string) string {
	return "system-sensors/sensor/" + devicename + "/state"
}

func availabilityTopic(devicename string) string {
	return "system-sensors/sensor/" + devicename + "/availability"
}
