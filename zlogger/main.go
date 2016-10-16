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

package main

import (
	"flag"

	"github.com/zephyr-im/zlogger"
)

var config = flag.String("config", "", "Path to the zlogger configuration file")

func main() {
	flag.Parse()
	zlog := zlogger.NewZLogger()
	zlog.RegisterSignalHandlers()
	zlog.Initialize(*config)
	defer zlog.Shutdown()
	zlog.Run()
}
