// Copyright 2014 The zephyr-go authors. All rights reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package zlogger

import (
	"container/list"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"path"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"

	"github.com/BurntSushi/toml"
	"github.com/zephyr-im/krb5-go"
	"github.com/zephyr-im/zephyr-go"
)

// Main configuration for the zlogger.
type Config struct {
	Logging map[string]Logger
}

type Logger struct {
	// Class is the zephyr class that is being logged.
	Class string

	// LogDirectory is the location where the zlogs will be placed.
	LogDirectory string

	// Mode specifies how the logs will be sharded. Currently, the
	// following modes are supported:
	//
	// * None - no sharding is performed, all messages will be
	//   logged to the same file. Files will be named with the
	//   class name.
	// * Day - Each day, a new log file will be started. Files
	//   will be named according to the pattern class.YYYY-MM-DD.
	// * Instance - Each instance will result in its own
	//   shard. Files will be named with the instance name.
	//
	// In the event that naming the file would result in special
	// characters (i.e., non-alphanumeric) to appear, the filename
	// "weird" will be chosen instead.
	Mode string
}

func IsAsciiPrintable(inp string) bool {
	for _, r := range inp {
		if r > unicode.MaxASCII || !unicode.IsPrint(r) {
			return false
		}
	}
	return true
}

func (entry *Logger) Filename(msg *zephyr.Message) string {
	if entry.Mode == "None" {
		return path.Join(entry.LogDirectory, entry.Class)
	}
	if entry.Mode == "Day" {
		return path.Join(entry.LogDirectory,
			fmt.Sprintf("%s.%s", entry.Class,
				msg.Header.UID.Time().Format("2006-01-02")))
	}
	if entry.Mode == "Instance" {
		instance := msg.Header.Instance
		if !IsAsciiPrintable(instance) || instance == "index.html" || instance == "index.shtml" ||
			strings.HasPrefix(instance, ".") || strings.Contains(instance, "/") {
			instance = "weird"
		}
		return path.Join(entry.LogDirectory, instance)
	}
	return ""
}

// ValidateConfig makes sure that there are no invalid settings
// specified. This primarily includes verifying that the logging modes
// are valid, and that the directories exist.
func (config *Config) Validate() error {
	// We need to make sure that the Mode strings are all valid.d
	valid := map[string]bool{
		"None":     true,
		"Day":      true,
		"Instance": true,
	}
	for name, log := range config.Logging {
		if _, present := valid[log.Mode]; !present {
			return errors.New(fmt.Sprintf("[%s] \"%s\" is not a valid mode.", name, log.Mode))
		}
		stat, err := os.Stat(log.LogDirectory)
		if err != nil {
			return err
		}
		if !stat.IsDir() {
			return errors.New(fmt.Sprintf("[%s] \"%s\" is not a directory.", name, log.LogDirectory))
		}
	}
	return nil
}

func (config *Config) ToSubscriptions() []zephyr.Subscription {
	uniqueClasses := map[string]bool{}
	for _, log := range config.Logging {
		uniqueClasses[log.Class] = true
	}
	subs := make([]zephyr.Subscription, len(uniqueClasses))
	i := 0
	for class, _ := range uniqueClasses {
		subs[i] = zephyr.Subscription{
			Class:     class,
			Instance:  zephyr.WildcardInstance,
			Recipient: "",
		}
		i++
	}
	return subs
}

type QuickMap map[string]*list.List

func (config *Config) ToQuickMap() QuickMap {
	quick := map[string]*list.List{}
	for _, lg := range config.Logging {
		if _, present := quick[lg.Class]; !present {
			quick[lg.Class] = list.New()
		}
		quick[lg.Class].PushBack(lg)
	}
	return quick
}

type ZLogger struct {
	// configPath is the path to the TOML configuration file
	configPath string
	// config is the actual, currently laoded config
	config *Config
	// lookup is a map taking class names to a List of Loggers.
	lookup QuickMap
	// configMu protects the configuration data. Use reader/writer
	// locks as appropriate
	configMu sync.RWMutex
	// A Zephyr Session
	session *zephyr.Session
}

func (logger *ZLogger) Initialize(configPath string) {
	// Initialization has a few steps -- first we need to store
	// the configuration file path, then we need to load the
	// configuration. We do not register our signal handlers here.
	logger.configPath = configPath
	if err := logger.LoadConfiguration(); err != nil {
		// We need an initial configuration file.
		log.Fatal(err)
	}
	session, err := zephyr.DialSystemDefault()
	if err != nil {
		log.Fatal(err)
	}
	logger.session = session
	go logger.MessageHandler()
	if err := logger.Subscribe(); err != nil {
		log.Fatal(err)
	}
}

func (logger *ZLogger) LoadConfiguration() error {
	log.Printf("Loading configuration from %s\n", logger.configPath)
	newConfig := &Config{}
	if _, err := toml.DecodeFile(logger.configPath, newConfig); err != nil {
		return err
	}
	if err := newConfig.Validate(); err != nil {
		return err
	}
	logger.configMu.Lock()
	defer logger.configMu.Unlock()
	logger.config = newConfig
	logger.lookup = logger.config.ToQuickMap()
	return nil
}

func (logger *ZLogger) RegisterSignalHandlers() {
	log.Println("Registering SIGHUP reload handler.")
	c := make(chan os.Signal, 1)
	signal.Notify(c, syscall.SIGHUP)
	go func() {
		for sig := range c {
			log.Printf("Received %v\n", sig)
			if err := logger.LoadConfiguration(); err != nil {
				log.Printf("Failed to reload configuration, continuing with old configuration: %s\n", err)
			}
		}
	}()
}

func (logger *ZLogger) Subscribe() error {
	logger.configMu.RLock()
	defer logger.configMu.RUnlock()
	subs := logger.config.ToSubscriptions()
	for _, sub := range subs {
		log.Printf("Subscribing to \"%s\"\n", sub.Class)
	}
	ctx, err := krb5.NewContext()
	if err != nil {
		log.Fatal(err)
	}
	defer ctx.Free()
	_, err = logger.session.SendSubscribeNoDefaults(ctx, subs)
	return err
}

func (logger *ZLogger) MessageHandler() {
	log.Printf("Starting message handler...\n")
	for r := range logger.session.Messages() {
		class := r.Message.Header.Class
		opcode := strings.ToUpper(r.Message.Header.OpCode)
		if opcode == "NOLOG" {
			log.Printf("Ignoring message to class \"%s\" due to nolog opcode.\n", class)
			continue
		}
		if opcode == "PING" {
			// Ugh, some versions of zwrite will send a ping to a class. Silently drop it
			continue
		}
		logger.configMu.RLock()
		defer logger.configMu.RUnlock()
		log.Printf("Handling zephyr to class \"%s\"\n", class)
		lst, present := logger.lookup[class]
		if !present {
			log.Printf("No configuration to log class \"%s\"\n", class)
			continue
		}
		for e := lst.Front(); e != nil; e = e.Next() {
			entry := e.Value.(Logger)
			filename := entry.Filename(r.Message)
			if filename == "" {
				continue
			}
			file, err := os.OpenFile(filename, os.O_RDWR|os.O_APPEND|os.O_CREATE, 0666)
			if err != nil {
				log.Printf("Cannot open file \"%s\": %s\n", filename, err)
				continue
			}
			_, err = file.WriteString(FormatMessage(r.Message))
			if err != nil {
				log.Printf("Cannot write to file \"%s\": %s\n", filename, err)
			}
			if err = file.Close(); err != nil {
				log.Printf("Could not close \"%s\": %s\n", filename, err)
			}
		}

	}
}

func (logger *ZLogger) Run() {
	log.Printf("Running logger")
	// Register a signal handler on SIGINT/SIGTERM and just block
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	<-c
}

func (logger *ZLogger) Shutdown() {
	log.Printf("Shutting down...")
	logger.session.Close()
}

func FormatMessage(msg *zephyr.Message) string {
	hostname := msg.Header.SenderAddress.String()
	rDNS, err := net.LookupAddr(hostname)
	if err == nil {
		hostname = rDNS[0]
	}
	fields := len(msg.Body)
	zsig := ""
	text := ""
	// If we have any fields at all, the message body is last. If
	// we have more than 1 field, then the zsig will be first.
	if fields > 0 {
		text = msg.Body[fields-1]
	}
	if fields > 1 {
		zsig = msg.Body[0]
	}
	return fmt.Sprintf("Instance: %s Time: %s Host: %s\n"+
		"From: %s <%s>\n"+
		"\n"+
		"%s\n",
		msg.Header.Instance,
		msg.Header.UID.Time().Format(time.UnixDate),
		hostname,
		zsig,
		msg.Header.Sender,
		text,
	)
}

func NewZLogger() *ZLogger {
	return &ZLogger{}
}
