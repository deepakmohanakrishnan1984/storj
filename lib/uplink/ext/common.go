// Copyright (C) 2019 Storj Labs, Inc.
// See LICENSE for copying information.

package main

// #cgo CFLAGS: -g -Wall
// #include <stdlib.h>
// #ifndef STORJ_HEADERS
//   #define STORJ_HEADERS
//   #include "c/headers/main.h"
// #endif
import "C"
import (
	"storj.io/storj/pkg/storj"
)

var structRefMap = newMapping()

//export GetIDVersion
func GetIDVersion(number C.uint, cErr **C.char) (cIDVersion C.IDVersion_t) {
	goIDVersion, err := storj.GetIDVersion(storj.IDVersionNumber(number))
	if err != nil {
		*cErr = C.CString(err.Error())
		return cIDVersion
	}

	return C.IDVersion_t{
		number: C.uint32_t(goIDVersion.Number),
	}
}