// Copyright (C) 2019 Storj Labs, Inc.
// See LICENSE for copying information.

package metainfo_test

import (
	"context"
	"sort"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zeebo/errs"

	"storj.io/storj/internal/testcontext"
	"storj.io/storj/internal/testplanet"
	"storj.io/storj/pkg/macaroon"
	"storj.io/storj/pkg/pb"
	"storj.io/storj/pkg/storj"
	"storj.io/storj/satellite/console"
)

// mockAPIKeys is mock for api keys store of pointerdb
type mockAPIKeys struct {
	info console.APIKeyInfo
	err  error
}

// GetByKey return api key info for given key
func (keys *mockAPIKeys) GetByKey(ctx context.Context, key macaroon.APIKey) (*console.APIKeyInfo, error) {
	return &keys.info, keys.err
}

func TestInvalidAPIKey(t *testing.T) {
	ctx := testcontext.New(t)
	defer ctx.Cleanup()

	planet, err := testplanet.New(t, 1, 1, 1)
	require.NoError(t, err)
	defer ctx.Check(planet.Shutdown)

	planet.Start(ctx)

	for _, invalidAPIKey := range []string{"", "invalid", "testKey"} {
		client, err := planet.Uplinks[0].DialMetainfo(ctx, planet.Satellites[0], invalidAPIKey)
		require.NoError(t, err)

		_, _, err = client.CreateSegment(ctx, "hello", "world", 1, &pb.RedundancyScheme{}, 123, time.Now())
		assertUnauthenticated(t, err, false)

		_, err = client.CommitSegment(ctx, "testbucket", "testpath", 0, &pb.Pointer{}, nil)
		assertUnauthenticated(t, err, false)

		_, err = client.SegmentInfo(ctx, "testbucket", "testpath", 0)
		assertUnauthenticated(t, err, false)

		_, _, err = client.ReadSegment(ctx, "testbucket", "testpath", 0)
		assertUnauthenticated(t, err, false)

		_, err = client.DeleteSegment(ctx, "testbucket", "testpath", 0)
		assertUnauthenticated(t, err, false)

		_, _, err = client.ListSegments(ctx, "testbucket", "", "", "", true, 1, 0)
		assertUnauthenticated(t, err, false)
	}
}

func TestRestrictedAPIKey(t *testing.T) {
	ctx := testcontext.New(t)
	defer ctx.Cleanup()

	planet, err := testplanet.New(t, 1, 1, 1)
	require.NoError(t, err)
	defer ctx.Check(planet.Shutdown)

	planet.Start(ctx)

	key, err := macaroon.ParseAPIKey(planet.Uplinks[0].APIKey[planet.Satellites[0].ID()])
	require.NoError(t, err)

	tests := []struct {
		Caveat               macaroon.Caveat
		CreateSegmentAllowed bool
		CommitSegmentAllowed bool
		SegmentInfoAllowed   bool
		ReadSegmentAllowed   bool
		DeleteSegmentAllowed bool
		ListSegmentsAllowed  bool
	}{
		{ // Everything disallowed
			Caveat: macaroon.Caveat{
				DisallowReads:   true,
				DisallowWrites:  true,
				DisallowLists:   true,
				DisallowDeletes: true,
			},
		},

		{ // Read only
			Caveat: macaroon.Caveat{
				DisallowWrites:  true,
				DisallowDeletes: true,
			},
			SegmentInfoAllowed:  true,
			ReadSegmentAllowed:  true,
			ListSegmentsAllowed: true,
		},

		{ // Write only
			Caveat: macaroon.Caveat{
				DisallowReads: true,
				DisallowLists: true,
			},
			CreateSegmentAllowed: true,
			CommitSegmentAllowed: true,
			DeleteSegmentAllowed: true,
		},

		{ // Bucket restriction
			Caveat: macaroon.Caveat{
				AllowedPaths: []*macaroon.Caveat_Path{{
					Bucket: []byte("otherbucket"),
				}},
			},
		},

		{ // Path restriction
			Caveat: macaroon.Caveat{
				AllowedPaths: []*macaroon.Caveat_Path{{
					Bucket:              []byte("testbucket"),
					EncryptedPathPrefix: []byte("otherpath"),
				}},
			},
		},

		{ // Time restriction after
			Caveat: macaroon.Caveat{
				NotAfter: func(x time.Time) *time.Time { return &x }(time.Now()),
			},
		},

		{ // Time restriction before
			Caveat: macaroon.Caveat{
				NotBefore: func(x time.Time) *time.Time { return &x }(time.Now().Add(time.Hour)),
			},
		},
	}

	for _, test := range tests {
		restrictedKey, err := key.Restrict(test.Caveat)
		require.NoError(t, err)

		client, err := planet.Uplinks[0].DialMetainfo(ctx, planet.Satellites[0], restrictedKey.Serialize())
		require.NoError(t, err)

		_, _, err = client.CreateSegment(ctx, "testbucket", "testpath", 1, &pb.RedundancyScheme{}, 123, time.Now())
		assertUnauthenticated(t, err, test.CreateSegmentAllowed)

		_, err = client.CommitSegment(ctx, "testbucket", "testpath", 0, &pb.Pointer{}, nil)
		assertUnauthenticated(t, err, test.CommitSegmentAllowed)

		_, err = client.SegmentInfo(ctx, "testbucket", "testpath", 0)
		assertUnauthenticated(t, err, test.SegmentInfoAllowed)

		_, _, err = client.ReadSegment(ctx, "testbucket", "testpath", 0)
		assertUnauthenticated(t, err, test.ReadSegmentAllowed)

		_, err = client.DeleteSegment(ctx, "testbucket", "testpath", 0)
		assertUnauthenticated(t, err, test.DeleteSegmentAllowed)

		_, _, err = client.ListSegments(ctx, "testbucket", "testpath", "", "", true, 1, 0)
		assertUnauthenticated(t, err, test.ListSegmentsAllowed)

	}
}

func assertUnauthenticated(t *testing.T, err error, allowed bool) {
	t.Helper()

	// If it's allowed, we allow any non-unauthenticated error because
	// some calls error after authentication checks.
	if err, ok := status.FromError(errs.Unwrap(err)); ok {
		assert.Equal(t, codes.Unauthenticated == err.Code(), !allowed)
	} else if !allowed {
		assert.Fail(t, "got unexpected error", "%T", err)
	}
}

func TestServiceList(t *testing.T) {
	ctx := testcontext.New(t)
	defer ctx.Cleanup()

	planet, err := testplanet.New(t, 1, 6, 1)
	require.NoError(t, err)
	defer ctx.Check(planet.Shutdown)

	planet.Start(ctx)

	items := []struct {
		Key   string
		Value []byte
	}{
		{Key: "sample.😶", Value: []byte{1}},
		{Key: "müsic", Value: []byte{2}},
		{Key: "müsic/söng1.mp3", Value: []byte{3}},
		{Key: "müsic/söng2.mp3", Value: []byte{4}},
		{Key: "müsic/album/söng3.mp3", Value: []byte{5}},
		{Key: "müsic/söng4.mp3", Value: []byte{6}},
		{Key: "ビデオ/movie.mkv", Value: []byte{7}},
	}

	for _, item := range items {
		err := planet.Uplinks[0].Upload(ctx, planet.Satellites[0], "testbucket", item.Key, item.Value)
		assert.NoError(t, err)
	}

	config := planet.Uplinks[0].GetConfig(planet.Satellites[0])
	metainfo, _, err := config.GetMetainfo(ctx, planet.Uplinks[0].Identity)
	require.NoError(t, err)

	type Test struct {
		Request  storj.ListOptions
		Expected storj.ObjectList // objects are partial
	}

	list, err := metainfo.ListObjects(ctx, "testbucket", storj.ListOptions{Recursive: true, Direction: storj.After})
	require.NoError(t, err)

	expected := []storj.Object{
		{Path: "müsic"},
		{Path: "müsic/album/söng3.mp3"},
		{Path: "müsic/söng1.mp3"},
		{Path: "müsic/söng2.mp3"},
		{Path: "müsic/söng4.mp3"},
		{Path: "sample.😶"},
		{Path: "ビデオ/movie.mkv"},
	}

	require.Equal(t, len(expected), len(list.Items))
	sort.Slice(list.Items, func(i, k int) bool {
		return list.Items[i].Path < list.Items[k].Path
	})
	for i, item := range expected {
		require.Equal(t, item.Path, list.Items[i].Path)
		require.Equal(t, item.IsPrefix, list.Items[i].IsPrefix)
	}

	list, err = metainfo.ListObjects(ctx, "testbucket", storj.ListOptions{Recursive: false, Direction: storj.After})
	require.NoError(t, err)

	expected = []storj.Object{
		{Path: "müsic"},
		{Path: "müsic/", IsPrefix: true},
		{Path: "sample.😶"},
		{Path: "ビデオ/", IsPrefix: true},
	}

	require.Equal(t, len(expected), len(list.Items))
	sort.Slice(list.Items, func(i, k int) bool {
		return list.Items[i].Path < list.Items[k].Path
	})
	for i, item := range expected {
		t.Log(item.Path, list.Items[i].Path)
		require.Equal(t, item.Path, list.Items[i].Path)
		require.Equal(t, item.IsPrefix, list.Items[i].IsPrefix)
	}
}

func TestCommitSegment(t *testing.T) {
	testplanet.Run(t, testplanet.Config{
		SatelliteCount: 1, StorageNodeCount: 6, UplinkCount: 1,
	}, func(t *testing.T, ctx *testcontext.Context, planet *testplanet.Planet) {
		apiKey := planet.Uplinks[0].APIKey[planet.Satellites[0].ID()]

		metainfo, err := planet.Uplinks[0].DialMetainfo(ctx, planet.Satellites[0], apiKey)
		require.NoError(t, err)

		{
			// error if pointer is nil
			_, err = metainfo.CommitSegment(ctx, "bucket", "path", -1, nil, []*pb.OrderLimit2{})
			require.Error(t, err)
		}
		{
			// error if bucket contains slash
			_, err = metainfo.CommitSegment(ctx, "bucket/storj", "path", -1, &pb.Pointer{}, []*pb.OrderLimit2{})
			require.Error(t, err)
		}
		{
			// error if number of remote pieces is lower then repair threshold
			redundancy := &pb.RedundancyScheme{
				MinReq:           1,
				RepairThreshold:  2,
				SuccessThreshold: 4,
				Total:            6,
				ErasureShareSize: 10,
			}
			addresedLimits, rootPieceID, err := metainfo.CreateSegment(ctx, "bucket", "path", -1, redundancy, 1000, time.Now())
			require.NoError(t, err)

			// create number of pieces below repair threshold
			usedForPieces := addresedLimits[:redundancy.RepairThreshold-1]
			pieces := make([]*pb.RemotePiece, len(usedForPieces))
			for i, limit := range usedForPieces {
				pieces[i] = &pb.RemotePiece{
					PieceNum: int32(i),
					NodeId:   limit.Limit.StorageNodeId,
				}
			}
			pointer := &pb.Pointer{
				Type: pb.Pointer_REMOTE,
				Remote: &pb.RemoteSegment{
					RootPieceId:  rootPieceID,
					Redundancy:   redundancy,
					RemotePieces: pieces,
				},
			}

			limits := make([]*pb.OrderLimit2, len(addresedLimits))
			for i, addresedLimit := range addresedLimits {
				limits[i] = addresedLimit.Limit
			}
			_, err = metainfo.CommitSegment(ctx, "bucket", "path", -1, pointer, limits)
			require.Error(t, err)
			require.Contains(t, err.Error(), "Number of valid pieces is less than or equal to the repair threshold")
		}
	})
}
