package main

import (
	_ "embed"
	"encoding/json"

	"github.com/nguyenvanduocit/agentd/daemon/home"
)

// mygoConfig is app/mygo.json, whose version `mygo build` gives the bundle: the version of agentd.app is the version
// of the engine in it, the command line included.
//
//go:embed mygo.json
var mygoConfig []byte

func init() {
	var c struct{ Version string }
	if json.Unmarshal(mygoConfig, &c) == nil && c.Version != "" {
		home.Version = c.Version
	}
}
