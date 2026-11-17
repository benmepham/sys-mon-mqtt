package main

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/shirou/gopsutil/v4/cpu"
	"gopkg.in/yaml.v3"
)

// Config mirrors the Python settings.yaml. Unknown keys are ignored.
type Config struct {
	MQTT struct {
		Hostname string `yaml:"hostname"`
		Port     int    `yaml:"port"`
		User     string `yaml:"user"`
		Password string `yaml:"password"`
	} `yaml:"mqtt"`
	TLS struct {
		CACerts  string `yaml:"ca_certs"`
		CertFile string `yaml:"certfile"`
		KeyFile  string `yaml:"keyfile"`
	} `yaml:"tls"`
	DeviceName     string         `yaml:"devicename"`
	ClientID       string         `yaml:"client_id"`
	HAStatus       string         `yaml:"ha_status"`
	UpdateInterval int            `yaml:"update_interval"`
	Sensors        map[string]any `yaml:"sensors"`
}

func main() {
	log.SetFlags(0) // systemd adds timestamps
	if len(os.Args) != 2 {
		log.Fatalf("usage: %s <settings.yaml>", os.Args[0])
	}
	cfg, err := loadConfig(os.Args[1])
	if err != nil {
		log.Fatal(err)
	}

	devicename := strings.ToLower(strings.ReplaceAll(cfg.DeviceName, " ", ""))
	device := newDevice(devicename, cfg.DeviceName)
	sensors := buildSensors(cfg)
	cpu.Percent(0, false) // prime so the first cpu_usage reading has a baseline

	// Wakes the main loop to publish state right after (re)connecting
	refresh := make(chan struct{}, 1)

	scheme := "tcp"
	if cfg.TLS.CACerts != "" {
		scheme = "ssl"
	}
	opts := mqtt.NewClientOptions()
	opts.AddBroker(fmt.Sprintf("%s://%s:%d", scheme, cfg.MQTT.Hostname, cfg.MQTT.Port))
	opts.SetClientID(cfg.ClientID)
	opts.SetUsername(cfg.MQTT.User)
	opts.SetPassword(cfg.MQTT.Password)
	opts.SetWill(availabilityTopic(devicename), "offline", 1, true)
	opts.SetAutoReconnect(true)
	opts.SetConnectRetry(true)
	opts.SetMaxReconnectInterval(time.Minute)
	if cfg.TLS.CACerts != "" {
		tlsConfig, err := loadTLS(cfg)
		if err != nil {
			log.Fatal(err)
		}
		opts.SetTLSConfig(tlsConfig)
	}
	opts.SetConnectionLostHandler(func(c mqtt.Client, err error) {
		log.Printf("connection lost: %v", err)
	})
	// Handlers must not wait on tokens, so publishes here are fire-and-forget.
	opts.SetOnConnectHandler(func(c mqtt.Client) {
		log.Printf("connected to %s", cfg.MQTT.Hostname)
		c.Subscribe(cfg.HAStatus+"/status", 1, func(c mqtt.Client, m mqtt.Message) {
			if string(m.Payload()) == "online" {
				log.Printf("Home Assistant online, resending discovery")
				publishDiscovery(c, devicename, device, sensors)
			}
		})
		publishDiscovery(c, devicename, device, sensors)
		select {
		case refresh <- struct{}{}:
		default:
		}
	})

	client := mqtt.NewClient(opts)
	client.Connect() // retries in the background until it succeeds

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	ticker := time.NewTicker(time.Duration(cfg.UpdateInterval) * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
		case <-refresh:
		case <-stop:
			log.Printf("shutting down")
			client.Publish(availabilityTopic(devicename), 1, true, "offline").WaitTimeout(2 * time.Second)
			client.Disconnect(250)
			return
		}
		if !client.IsConnectionOpen() {
			continue
		}
		publishState(client, devicename, sensors)
	}
}

func loadConfig(path string) (Config, error) {
	var cfg Config
	b, err := os.ReadFile(path)
	if err != nil {
		return cfg, err
	}
	// Lowercase top-level keys like the Python version, so deviceName works
	var doc yaml.Node
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return cfg, err
	}
	if len(doc.Content) > 0 {
		root := doc.Content[0]
		for i := 0; i < len(root.Content); i += 2 {
			root.Content[i].Value = strings.ToLower(root.Content[i].Value)
		}
		if err := root.Decode(&cfg); err != nil {
			return cfg, err
		}
	}

	if cfg.MQTT.Port == 0 {
		cfg.MQTT.Port = 1883
	}
	if cfg.HAStatus == "" {
		cfg.HAStatus = "hass"
	}
	if cfg.UpdateInterval <= 0 {
		cfg.UpdateInterval = 60
	}
	if cfg.MQTT.Hostname == "" || cfg.DeviceName == "" || cfg.ClientID == "" {
		return cfg, errors.New("settings need mqtt.hostname, deviceName and client_id")
	}
	return cfg, nil
}

func loadTLS(cfg Config) (*tls.Config, error) {
	ca, err := os.ReadFile(cfg.TLS.CACerts)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca) {
		return nil, errors.New("no certificates found in " + cfg.TLS.CACerts)
	}
	tlsConfig := &tls.Config{RootCAs: pool}
	if cfg.TLS.CertFile != "" && cfg.TLS.KeyFile != "" {
		cert, err := tls.LoadX509KeyPair(cfg.TLS.CertFile, cfg.TLS.KeyFile)
		if err != nil {
			return nil, err
		}
		tlsConfig.Certificates = []tls.Certificate{cert}
	}
	return tlsConfig, nil
}

// publishState reads every sensor and publishes one JSON payload. Failed sensors are left out.
func publishState(client mqtt.Client, devicename string, sensors []Sensor) {
	values := map[string]any{}
	for _, s := range sensors {
		value, err := readSafely(s)
		if err != nil {
			log.Printf("sensor %s: %v", s.Key, err)
			continue
		}
		values[s.Key] = value
	}
	payload, err := json.Marshal(values)
	if err != nil {
		log.Printf("state: %v", err)
		return
	}
	client.Publish(stateTopic(devicename), 1, false, payload)
}

// readSafely stops a panicking sensor from crashing the program.
func readSafely(s Sensor) (value any, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	return s.Read()
}
