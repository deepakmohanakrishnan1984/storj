// Copyright (C) 2019 Storj Labs, Inc.
// See LICENSE for copying information.

package main

// #cgo CFLAGS: -g -Wall
// #ifndef STORJ_HEADERS
//   #define STORJ_HEADERS
//   #include "c/headers/main.h"
// #endif
import "C"
import (
	"context"
	"gopkg.in/spacemonkeygo/monkit.v2"

	"storj.io/storj/lib/uplink"
)

var mon = monkit.Package()

//export NewUplink
func NewUplink(cErr **C.char) (cUplink C.UplinkRef_t) {
	goUplink, err := uplink.NewUplink(context.Background(), &uplink.Config{})
	if err != nil {
		*cErr = C.CString(err.Error())
		return cUplink
	}

	return C.UplinkRef_t(structRefMap.Add(goUplink))
}

//export NewUplinkInsecure
func NewUplinkInsecure(cErr **C.char) (cUplink C.UplinkRef_t) {
	insecureConfig := &uplink.Config{}
	insecureConfig.Volatile.TLS.SkipPeerCAWhitelist = true
	goUplink, err := uplink.NewUplink(context.Background(), insecureConfig)
	if err != nil {
		*cErr = C.CString(err.Error())
		return cUplink
	}

	return C.UplinkRef_t(structRefMap.Add(goUplink))
}

//export OpenProject
func OpenProject(cUplink C.UplinkRef_t, satelliteAddr *C.char, cAPIKey C.APIKeyRef_t, cErr **C.char) (cProject C.ProjectRef_t) {
	var err error
	ctx := context.Background()
	defer mon.Task()(&ctx)(&err)

	goUplink, ok := structRefMap.Get(token(cUplink)).(*uplink.Uplink)
	if !ok {
		*cErr = C.CString("invalid uplink")
		return cProject
	}

	apiKey, ok := structRefMap.Get(token(cAPIKey)).(uplink.APIKey)
	if !ok {
		*cErr = C.CString("invalid API Key")
		return cProject
	}

	// TODO: add project options argument
	project, err := goUplink.OpenProject(ctx, C.GoString(satelliteAddr), apiKey, nil)
	if err != nil {
		*cErr = C.CString(err.Error())
		return cProject
	}
	return C.ProjectRef_t(structRefMap.Add(project))
}
