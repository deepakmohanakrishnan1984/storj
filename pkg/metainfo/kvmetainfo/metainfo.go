// Copyright (C) 2019 Storj Labs, Inc.
// See LICENSE for copying information.

package kvmetainfo

import (
	"github.com/zeebo/errs"
	monkit "gopkg.in/spacemonkeygo/monkit.v2"

	"storj.io/storj/internal/memory"
	"storj.io/storj/pkg/eestream"
	"storj.io/storj/pkg/storage/buckets"
	"storj.io/storj/pkg/storage/segments"
	"storj.io/storj/pkg/storage/streams"
	"storj.io/storj/pkg/storj"
	"storj.io/storj/storage"
	"storj.io/storj/uplink/metainfo"
)

var mon = monkit.Package()

var errClass = errs.Class("kvmetainfo")

const defaultSegmentLimit = 8 // TODO

var _ storj.Metainfo = (*DB)(nil)

// DB implements metainfo database
type DB struct {
	*Project

	metainfo           metainfo.Client
	buckets            buckets.Store
	streams            streams.Store
	segments           segments.Store
	rootKey            *storj.Key
	encryptedBlockSize int32
}

// New creates a new metainfo database
func New(metainfo metainfo.Client, buckets buckets.Store, streams streams.Store, segments segments.Store, rootKey *storj.Key, encryptedBlockSize int32, redundancy eestream.RedundancyStrategy, segmentsSize int64) *DB {
	return &DB{
		Project:            NewProject(metainfo, redundancy, segmentsSize),
		metainfo:           metainfo,
		buckets:            buckets,
		streams:            streams,
		segments:           segments,
		rootKey:            rootKey,
		encryptedBlockSize: encryptedBlockSize,
	}
}

// Limits returns limits for this metainfo database
func (db *DB) Limits() (storj.MetainfoLimits, error) {
	return storj.MetainfoLimits{
		ListLimit:                storage.LookupLimit,
		MinimumRemoteSegmentSize: memory.KiB.Int64(), // TODO: is this needed here?
		MaximumInlineSegmentSize: memory.MiB.Int64(),
	}, nil
}
