// Copyright (C) 2019 Storj Labs, Inc.
// See LICENSE for copying information

package kademlia

import (
	"bytes"
	"sync"
	"testing"
	"time"

	"github.com/gogo/protobuf/proto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"storj.io/storj/internal/testcontext"
	"storj.io/storj/internal/teststorj"
	"storj.io/storj/pkg/overlay"
	"storj.io/storj/pkg/pb"
	"storj.io/storj/pkg/storj"
	"storj.io/storj/storage"
	"storj.io/storj/storage/storelogger"
	"storj.io/storj/storage/teststore"
)

type routingTableOpts struct {
	bucketSize int
	cacheSize  int
}

// newTestRoutingTable returns a newly configured instance of a RoutingTable
func newTestRoutingTable(local *overlay.NodeDossier, opts routingTableOpts) (*RoutingTable, error) {
	if opts.bucketSize == 0 {
		opts.bucketSize = 6
	}
	if opts.cacheSize == 0 {
		opts.cacheSize = 2
	}
	rt := &RoutingTable{
		self:         local,
		kadBucketDB:  storelogger.New(zap.L().Named("rt.kad"), teststore.New()),
		nodeBucketDB: storelogger.New(zap.L().Named("rt.node"), teststore.New()),
		transport:    &defaultTransport,

		mutex:            &sync.Mutex{},
		rcMutex:          &sync.Mutex{},
		seen:             make(map[storj.NodeID]*pb.Node),
		replacementCache: make(map[bucketID][]*pb.Node),

		bucketSize:   opts.bucketSize,
		rcBucketSize: opts.cacheSize,
	}
	ok, err := rt.addNode(&local.Node)
	if !ok || err != nil {
		return nil, RoutingErr.New("could not add localNode to routing table: %s", err)
	}
	return rt, nil
}

func createRoutingTableWith(localNodeID storj.NodeID, opts routingTableOpts) *RoutingTable {
	if localNodeID == (storj.NodeID{}) {
		panic("empty local node id")
	}
	local := &overlay.NodeDossier{Node: pb.Node{Id: localNodeID}}

	rt, err := newTestRoutingTable(local, opts)
	if err != nil {
		panic(err)
	}
	return rt
}

func createRoutingTable(localNodeID storj.NodeID) *RoutingTable {
	return createRoutingTableWith(localNodeID, routingTableOpts{})
}

func TestAddNode(t *testing.T) {
	ctx := testcontext.New(t)
	defer ctx.Cleanup()
	rt := createRoutingTable(teststorj.NodeIDFromString("OO"))
	defer ctx.Check(rt.Close)

	cases := []struct {
		testID  string
		node    *pb.Node
		added   bool
		kadIDs  [][]byte
		nodeIDs [][]string
	}{
		{testID: "PO: add node to unfilled kbucket",
			node:    teststorj.MockNode("PO"),
			added:   true,
			kadIDs:  [][]byte{{255, 255}},
			nodeIDs: [][]string{{"OO", "PO"}},
		},
		{testID: "NO: add node to full kbucket and split",
			node:    teststorj.MockNode("NO"),
			added:   true,
			kadIDs:  [][]byte{{255, 255}},
			nodeIDs: [][]string{{"NO", "OO", "PO"}},
		},
		{testID: "MO",
			node:    teststorj.MockNode("MO"),
			added:   true,
			kadIDs:  [][]byte{{255, 255}},
			nodeIDs: [][]string{{"MO", "NO", "OO", "PO"}},
		},
		{testID: "LO",
			node:    teststorj.MockNode("LO"),
			added:   true,
			kadIDs:  [][]byte{{255, 255}},
			nodeIDs: [][]string{{"LO", "MO", "NO", "OO", "PO"}},
		},
		{testID: "QO",
			node:    teststorj.MockNode("QO"),
			added:   true,
			kadIDs:  [][]byte{{255, 255}},
			nodeIDs: [][]string{{"LO", "MO", "NO", "OO", "PO", "QO"}},
		},
		{testID: "SO: split bucket",
			node:    teststorj.MockNode("SO"),
			added:   true,
			kadIDs:  [][]byte{{63, 255}, {79, 255}, {95, 255}, {127, 255}, {255, 255}},
			nodeIDs: [][]string{{}, {"LO", "MO", "NO", "OO"}, {"PO", "QO", "SO"}, {}, {}},
		},
		{testID: "?O",
			node:    teststorj.MockNode("?O"),
			added:   true,
			kadIDs:  [][]byte{{63, 255}, {79, 255}, {95, 255}, {127, 255}, {255, 255}},
			nodeIDs: [][]string{{"?O"}, {"LO", "MO", "NO", "OO"}, {"PO", "QO", "SO"}, {}, {}},
		},
		{testID: ">O",
			node:   teststorj.MockNode(">O"),
			added:  true,
			kadIDs: [][]byte{{63, 255}, {79, 255}, {95, 255}, {127, 255}, {255, 255}}, nodeIDs: [][]string{{">O", "?O"}, {"LO", "MO", "NO", "OO"}, {"PO", "QO", "SO"}, {}, {}},
		},
		{testID: "=O",
			node:    teststorj.MockNode("=O"),
			added:   true,
			kadIDs:  [][]byte{{63, 255}, {79, 255}, {95, 255}, {127, 255}, {255, 255}},
			nodeIDs: [][]string{{"=O", ">O", "?O"}, {"LO", "MO", "NO", "OO"}, {"PO", "QO", "SO"}, {}, {}},
		},
		{testID: ";O",
			node:    teststorj.MockNode(";O"),
			added:   true,
			kadIDs:  [][]byte{{63, 255}, {79, 255}, {95, 255}, {127, 255}, {255, 255}},
			nodeIDs: [][]string{{";O", "=O", ">O", "?O"}, {"LO", "MO", "NO", "OO"}, {"PO", "QO", "SO"}, {}, {}},
		},
		{testID: ":O",
			node:    teststorj.MockNode(":O"),
			added:   true,
			kadIDs:  [][]byte{{63, 255}, {79, 255}, {95, 255}, {127, 255}, {255, 255}},
			nodeIDs: [][]string{{":O", ";O", "=O", ">O", "?O"}, {"LO", "MO", "NO", "OO"}, {"PO", "QO", "SO"}, {}, {}},
		},
		{testID: "9O",
			node:    teststorj.MockNode("9O"),
			added:   true,
			kadIDs:  [][]byte{{63, 255}, {79, 255}, {95, 255}, {127, 255}, {255, 255}},
			nodeIDs: [][]string{{"9O", ":O", ";O", "=O", ">O", "?O"}, {"LO", "MO", "NO", "OO"}, {"PO", "QO", "SO"}, {}, {}},
		},
		{testID: "8O: should drop",
			node:    teststorj.MockNode("8O"),
			added:   false,
			kadIDs:  [][]byte{{63, 255}, {79, 255}, {95, 255}, {127, 255}, {255, 255}},
			nodeIDs: [][]string{{"9O", ":O", ";O", "=O", ">O", "?O"}, {"LO", "MO", "NO", "OO"}, {"PO", "QO", "SO"}, {}, {}},
		},
		{testID: "KO",
			node:    teststorj.MockNode("KO"),
			added:   true,
			kadIDs:  [][]byte{{63, 255}, {79, 255}, {95, 255}, {127, 255}, {255, 255}},
			nodeIDs: [][]string{{"9O", ":O", ";O", "=O", ">O", "?O"}, {"KO", "LO", "MO", "NO", "OO"}, {"PO", "QO", "SO"}, {}, {}},
		},
		{testID: "JO",
			node:    teststorj.MockNode("JO"),
			added:   true,
			kadIDs:  [][]byte{{63, 255}, {79, 255}, {95, 255}, {127, 255}, {255, 255}},
			nodeIDs: [][]string{{"9O", ":O", ";O", "=O", ">O", "?O"}, {"JO", "KO", "LO", "MO", "NO", "OO"}, {"PO", "QO", "SO"}, {}, {}},
		},
		{testID: "]O",
			node:    teststorj.MockNode("]O"),
			added:   true,
			kadIDs:  [][]byte{{63, 255}, {79, 255}, {95, 255}, {127, 255}, {255, 255}},
			nodeIDs: [][]string{{"9O", ":O", ";O", "=O", ">O", "?O"}, {"JO", "KO", "LO", "MO", "NO", "OO"}, {"PO", "QO", "SO", "]O"}, {}, {}},
		},
		{testID: "^O",
			node:    teststorj.MockNode("^O"),
			added:   true,
			kadIDs:  [][]byte{{63, 255}, {79, 255}, {95, 255}, {127, 255}, {255, 255}},
			nodeIDs: [][]string{{"9O", ":O", ";O", "=O", ">O", "?O"}, {"JO", "KO", "LO", "MO", "NO", "OO"}, {"PO", "QO", "SO", "]O", "^O"}, {}, {}},
		},
		{testID: "_O",
			node:    teststorj.MockNode("_O"),
			added:   true,
			kadIDs:  [][]byte{{63, 255}, {79, 255}, {95, 255}, {127, 255}, {255, 255}},
			nodeIDs: [][]string{{"9O", ":O", ";O", "=O", ">O", "?O"}, {"JO", "KO", "LO", "MO", "NO", "OO"}, {"PO", "QO", "SO", "]O", "^O", "_O"}, {}, {}},
		},
		{testID: "@O: split bucket 2",
			node:    teststorj.MockNode("@O"),
			added:   true,
			kadIDs:  [][]byte{{63, 255}, {71, 255}, {79, 255}, {95, 255}, {127, 255}, {255, 255}},
			nodeIDs: [][]string{{"9O", ":O", ";O", "=O", ">O", "?O"}, {"@O"}, {"JO", "KO", "LO", "MO", "NO", "OO"}, {"PO", "QO", "SO", "]O", "^O", "_O"}, {}, {}},
		},
	}
	for _, c := range cases {
		testCase := c
		t.Run(testCase.testID, func(t *testing.T) {
			ok, err := rt.addNode(testCase.node)
			require.NoError(t, err)
			require.Equal(t, testCase.added, ok)
			kadKeys, err := rt.kadBucketDB.List(nil, 0)
			require.NoError(t, err)
			for i, v := range kadKeys {
				require.True(t, bytes.Equal(testCase.kadIDs[i], v[:2]))
				ids, err := rt.getNodeIDsWithinKBucket(keyToBucketID(v))
				require.NoError(t, err)
				require.True(t, len(ids) == len(testCase.nodeIDs[i]))
				for j, id := range ids {
					require.True(t, bytes.Equal(teststorj.NodeIDFromString(testCase.nodeIDs[i][j]).Bytes(), id.Bytes()))
				}
			}

			if testCase.testID == "8O" {
				nodeID80 := teststorj.NodeIDFromString("8O")
				n := rt.replacementCache[keyToBucketID(nodeID80.Bytes())]
				require.Equal(t, nodeID80.Bytes(), n[0].Id.Bytes())
			}

		})
	}
}

func TestUpdateNode(t *testing.T) {
	ctx := testcontext.New(t)
	defer ctx.Cleanup()
	rt := createRoutingTable(teststorj.NodeIDFromString("AA"))
	defer ctx.Check(rt.Close)
	node := teststorj.MockNode("BB")
	ok, err := rt.addNode(node)
	assert.True(t, ok)
	assert.NoError(t, err)
	val, err := rt.nodeBucketDB.Get(node.Id.Bytes())
	assert.NoError(t, err)
	unmarshaled, err := unmarshalNodes([]storage.Value{val})
	assert.NoError(t, err)
	x := unmarshaled[0].Address
	assert.Nil(t, x)

	node.Address = &pb.NodeAddress{Address: "BB"}
	err = rt.updateNode(node)
	assert.NoError(t, err)
	val, err = rt.nodeBucketDB.Get(node.Id.Bytes())
	assert.NoError(t, err)
	unmarshaled, err = unmarshalNodes([]storage.Value{val})
	assert.NoError(t, err)
	y := unmarshaled[0].Address.Address
	assert.Equal(t, "BB", y)
}

func TestRemoveNode(t *testing.T) {
	ctx := testcontext.New(t)
	defer ctx.Cleanup()
	rt := createRoutingTable(teststorj.NodeIDFromString("AA"))
	defer ctx.Check(rt.Close)
	kadBucketID := firstBucketID
	node := teststorj.MockNode("BB")
	ok, err := rt.addNode(node)
	assert.True(t, ok)
	assert.NoError(t, err)
	val, err := rt.nodeBucketDB.Get(node.Id.Bytes())
	assert.NoError(t, err)
	assert.NotNil(t, val)
	node2 := teststorj.MockNode("CC")
	rt.addToReplacementCache(kadBucketID, node2)
	err = rt.removeNode(node)
	assert.NoError(t, err)
	val, err = rt.nodeBucketDB.Get(node.Id.Bytes())
	assert.Nil(t, val)
	assert.Error(t, err)
	val2, err := rt.nodeBucketDB.Get(node2.Id.Bytes())
	assert.NoError(t, err)
	assert.NotNil(t, val2)
	assert.Equal(t, 0, len(rt.replacementCache[kadBucketID]))

	//try to remove node not in rt
	err = rt.removeNode(&pb.Node{
		Id:      teststorj.NodeIDFromString("DD"),
		Address: &pb.NodeAddress{Address: "address:1"},
	})
	assert.NoError(t, err)
}

func TestCreateOrUpdateKBucket(t *testing.T) {
	ctx := testcontext.New(t)
	defer ctx.Cleanup()
	id := bucketID{255, 255}
	rt := createRoutingTable(teststorj.NodeIDFromString("AA"))
	defer ctx.Check(rt.Close)
	err := rt.createOrUpdateKBucket(id, time.Now())
	assert.NoError(t, err)
	val, e := rt.kadBucketDB.Get(id[:])
	assert.NotNil(t, val)
	assert.NoError(t, e)

}

func TestGetKBucketID(t *testing.T) {
	ctx := testcontext.New(t)
	defer ctx.Cleanup()
	kadIDA := bucketID{255, 255}
	nodeIDA := teststorj.NodeIDFromString("AA")
	rt := createRoutingTable(nodeIDA)
	defer ctx.Check(rt.Close)
	keyA, err := rt.getKBucketID(nodeIDA)
	assert.NoError(t, err)
	assert.Equal(t, kadIDA[:2], keyA[:2])
}

func TestWouldBeInNearestK(t *testing.T) {
	ctx := testcontext.New(t)
	defer ctx.Cleanup()
	rt := createRoutingTableWith(storj.NodeID{127, 255}, routingTableOpts{bucketSize: 2})
	defer ctx.Check(rt.Close)

	cases := []struct {
		testID  string
		nodeID  storj.NodeID
		closest bool
	}{
		{testID: "A",
			nodeID:  storj.NodeID{127, 255}, //XOR from [127, 255] is 0
			closest: true,
		},
		{testID: "B",
			nodeID:  storj.NodeID{143, 255}, //XOR from [127, 255] is 240
			closest: true,
		},
		{testID: "C",
			nodeID:  storj.NodeID{255, 255}, //XOR from [127, 255] is 128
			closest: true,
		},
		{testID: "D",
			nodeID:  storj.NodeID{191, 255}, //XOR from [127, 255] is 192
			closest: false,
		},
		{testID: "E",
			nodeID:  storj.NodeID{133, 255}, //XOR from [127, 255] is 250
			closest: false,
		},
	}
	for _, c := range cases {
		testCase := c
		t.Run(testCase.testID, func(t *testing.T) {
			result, err := rt.wouldBeInNearestK(testCase.nodeID)
			assert.NoError(t, err)
			assert.Equal(t, testCase.closest, result)
			assert.NoError(t, rt.nodeBucketDB.Put(testCase.nodeID.Bytes(), []byte("")))
		})
	}
}

func TestKadBucketContainsLocalNode(t *testing.T) {
	ctx := testcontext.New(t)
	defer ctx.Cleanup()
	nodeIDA := storj.NodeID{183, 255} //[10110111, 1111111]
	rt := createRoutingTable(nodeIDA)
	defer ctx.Check(rt.Close)
	kadIDA := firstBucketID
	var kadIDB bucketID
	copy(kadIDB[:], kadIDA[:])
	kadIDB[0] = 127
	now := time.Now()
	err := rt.createOrUpdateKBucket(kadIDB, now)
	assert.NoError(t, err)
	resultTrue, err := rt.kadBucketContainsLocalNode(kadIDA)
	assert.NoError(t, err)
	resultFalse, err := rt.kadBucketContainsLocalNode(kadIDB)
	assert.NoError(t, err)
	assert.True(t, resultTrue)
	assert.False(t, resultFalse)
}

func TestKadBucketHasRoom(t *testing.T) {
	ctx := testcontext.New(t)
	defer ctx.Cleanup()
	node1 := storj.NodeID{255, 255}
	rt := createRoutingTable(node1)
	defer ctx.Check(rt.Close)
	kadIDA := firstBucketID
	node2 := storj.NodeID{191, 255}
	node3 := storj.NodeID{127, 255}
	node4 := storj.NodeID{63, 255}
	node5 := storj.NodeID{159, 255}
	node6 := storj.NodeID{0, 127}
	resultA, err := rt.kadBucketHasRoom(kadIDA)
	assert.NoError(t, err)
	assert.True(t, resultA)
	assert.NoError(t, rt.nodeBucketDB.Put(node2.Bytes(), []byte("")))
	assert.NoError(t, rt.nodeBucketDB.Put(node3.Bytes(), []byte("")))
	assert.NoError(t, rt.nodeBucketDB.Put(node4.Bytes(), []byte("")))
	assert.NoError(t, rt.nodeBucketDB.Put(node5.Bytes(), []byte("")))
	assert.NoError(t, rt.nodeBucketDB.Put(node6.Bytes(), []byte("")))
	resultB, err := rt.kadBucketHasRoom(kadIDA)
	assert.NoError(t, err)
	assert.False(t, resultB)
}

func TestGetNodeIDsWithinKBucket(t *testing.T) {
	ctx := testcontext.New(t)
	defer ctx.Cleanup()
	nodeIDA := storj.NodeID{183, 255} //[10110111, 1111111]
	rt := createRoutingTable(nodeIDA)
	defer ctx.Check(rt.Close)
	kadIDA := firstBucketID
	var kadIDB bucketID
	copy(kadIDB[:], kadIDA[:])
	kadIDB[0] = 127
	now := time.Now()
	assert.NoError(t, rt.createOrUpdateKBucket(kadIDB, now))

	nodeIDB := storj.NodeID{111, 255} //[01101111, 1111111]
	nodeIDC := storj.NodeID{47, 255}  //[00101111, 1111111]

	assert.NoError(t, rt.nodeBucketDB.Put(nodeIDB.Bytes(), []byte("")))
	assert.NoError(t, rt.nodeBucketDB.Put(nodeIDC.Bytes(), []byte("")))

	cases := []struct {
		testID   string
		kadID    bucketID
		expected storage.Keys
	}{
		{testID: "A",
			kadID:    kadIDA,
			expected: storage.Keys{nodeIDA.Bytes()},
		},
		{testID: "B",
			kadID:    kadIDB,
			expected: storage.Keys{nodeIDC.Bytes(), nodeIDB.Bytes()},
		},
	}
	for _, c := range cases {
		testCase := c
		t.Run(testCase.testID, func(t *testing.T) {
			n, err := rt.getNodeIDsWithinKBucket(testCase.kadID)
			assert.NoError(t, err)
			for i, id := range testCase.expected {
				assert.True(t, id.Equal(n[i].Bytes()))
			}
		})
	}
}

func TestGetNodesFromIDs(t *testing.T) {
	ctx := testcontext.New(t)
	defer ctx.Cleanup()
	nodeA := teststorj.MockNode("AA")
	nodeB := teststorj.MockNode("BB")
	nodeC := teststorj.MockNode("CC")
	a, err := proto.Marshal(nodeA)
	assert.NoError(t, err)
	b, err := proto.Marshal(nodeB)
	assert.NoError(t, err)
	c, err := proto.Marshal(nodeC)
	assert.NoError(t, err)
	rt := createRoutingTable(nodeA.Id)
	defer ctx.Check(rt.Close)

	assert.NoError(t, rt.nodeBucketDB.Put(nodeA.Id.Bytes(), a))
	assert.NoError(t, rt.nodeBucketDB.Put(nodeB.Id.Bytes(), b))
	assert.NoError(t, rt.nodeBucketDB.Put(nodeC.Id.Bytes(), c))
	expected := []*pb.Node{nodeA, nodeB, nodeC}

	nodeKeys, err := rt.nodeBucketDB.List(nil, 0)
	assert.NoError(t, err)
	values, err := rt.getNodesFromIDsBytes(teststorj.NodeIDsFromBytes(nodeKeys.ByteSlices()...))
	assert.NoError(t, err)
	for i, n := range expected {
		assert.True(t, bytes.Equal(n.Id.Bytes(), values[i].Id.Bytes()))
	}
}

func TestUnmarshalNodes(t *testing.T) {
	ctx := testcontext.New(t)
	defer ctx.Cleanup()
	nodeA := teststorj.MockNode("AA")
	nodeB := teststorj.MockNode("BB")
	nodeC := teststorj.MockNode("CC")

	a, err := proto.Marshal(nodeA)
	assert.NoError(t, err)
	b, err := proto.Marshal(nodeB)
	assert.NoError(t, err)
	c, err := proto.Marshal(nodeC)
	assert.NoError(t, err)
	rt := createRoutingTable(nodeA.Id)
	defer ctx.Check(rt.Close)
	assert.NoError(t, rt.nodeBucketDB.Put(nodeA.Id.Bytes(), a))
	assert.NoError(t, rt.nodeBucketDB.Put(nodeB.Id.Bytes(), b))
	assert.NoError(t, rt.nodeBucketDB.Put(nodeC.Id.Bytes(), c))
	nodeKeys, err := rt.nodeBucketDB.List(nil, 0)
	assert.NoError(t, err)
	nodes, err := rt.getNodesFromIDsBytes(teststorj.NodeIDsFromBytes(nodeKeys.ByteSlices()...))
	assert.NoError(t, err)
	expected := []*pb.Node{nodeA, nodeB, nodeC}
	for i, v := range expected {
		assert.True(t, bytes.Equal(v.Id.Bytes(), nodes[i].Id.Bytes()))
	}
}

func TestGetUnmarshaledNodesFromBucket(t *testing.T) {
	ctx := testcontext.New(t)
	defer ctx.Cleanup()
	nodeA := teststorj.MockNode("AA")
	rt := createRoutingTable(nodeA.Id)
	defer ctx.Check(rt.Close)
	bucketID := firstBucketID
	nodeB := teststorj.MockNode("BB")
	nodeC := teststorj.MockNode("CC")
	var err error
	_, err = rt.addNode(nodeB)
	assert.NoError(t, err)
	_, err = rt.addNode(nodeC)
	assert.NoError(t, err)
	nodes, err := rt.getUnmarshaledNodesFromBucket(bucketID)
	expected := []*pb.Node{nodeA, nodeB, nodeC}
	assert.NoError(t, err)
	for i, v := range expected {
		assert.True(t, bytes.Equal(v.Id.Bytes(), nodes[i].Id.Bytes()))
	}
}

func TestGetKBucketRange(t *testing.T) {
	ctx := testcontext.New(t)
	defer ctx.Cleanup()
	rt := createRoutingTable(teststorj.NodeIDFromString("AA"))
	defer ctx.Check(rt.Close)
	idA := storj.NodeID{255, 255}
	idB := storj.NodeID{127, 255}
	idC := storj.NodeID{63, 255}
	assert.NoError(t, rt.kadBucketDB.Put(idA.Bytes(), []byte("")))
	assert.NoError(t, rt.kadBucketDB.Put(idB.Bytes(), []byte("")))
	assert.NoError(t, rt.kadBucketDB.Put(idC.Bytes(), []byte("")))
	zeroBID := bucketID{}
	cases := []struct {
		testID   string
		id       storj.NodeID
		expected storage.Keys
	}{
		{testID: "A",
			id:       idA,
			expected: storage.Keys{idB.Bytes(), idA.Bytes()},
		},
		{testID: "B",
			id:       idB,
			expected: storage.Keys{idC.Bytes(), idB.Bytes()}},
		{testID: "C",
			id:       idC,
			expected: storage.Keys{zeroBID[:], idC.Bytes()},
		},
	}
	for _, c := range cases {
		testCase := c
		t.Run(testCase.testID, func(t *testing.T) {
			ep, err := rt.getKBucketRange(keyToBucketID(testCase.id.Bytes()))
			assert.NoError(t, err)
			for i, k := range testCase.expected {
				assert.True(t, k.Equal(ep[i][:]))
			}
		})
	}
}

func TestBucketIDZeroValue(t *testing.T) {
	zero := bucketID{}
	expected := []byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}
	assert.True(t, bytes.Equal(zero[:], expected))
}

func TestDetermineLeafDepth(t *testing.T) {
	ctx := testcontext.New(t)
	defer ctx.Cleanup()
	rt := createRoutingTable(teststorj.NodeIDFromString("AA"))
	defer ctx.Check(rt.Close)
	idA, idB, idC := firstBucketID, firstBucketID, firstBucketID
	idA[0] = 255
	idB[0] = 127
	idC[0] = 63

	cases := []struct {
		testID  string
		id      storj.NodeID
		depth   int
		addNode func()
	}{
		{testID: "A",
			id:    idA,
			depth: 0,
			addNode: func() {
				e := rt.kadBucketDB.Put(idA.Bytes(), []byte(""))
				assert.NoError(t, e)
			},
		},
		{testID: "B",
			id:    idB,
			depth: 1,
			addNode: func() {
				e := rt.kadBucketDB.Put(idB.Bytes(), []byte(""))
				assert.NoError(t, e)
			},
		},
		{testID: "C",
			id:    idA,
			depth: 1,
			addNode: func() {
				e := rt.kadBucketDB.Put(idC.Bytes(), []byte(""))
				assert.NoError(t, e)
			},
		},
		{testID: "D",
			id:      idB,
			depth:   2,
			addNode: func() {},
		},
		{testID: "E",
			id:      idC,
			depth:   2,
			addNode: func() {},
		},
	}
	for _, c := range cases {
		testCase := c
		t.Run(testCase.testID, func(t *testing.T) {
			testCase.addNode()
			d, err := rt.determineLeafDepth(testCase.id)
			assert.NoError(t, err)
			assert.Equal(t, testCase.depth, d)
		})
	}
}

func TestSplitBucket(t *testing.T) {
	ctx := testcontext.New(t)
	defer ctx.Cleanup()
	rt := createRoutingTable(teststorj.NodeIDFromString("AA"))
	defer ctx.Check(rt.Close)
	cases := []struct {
		testID string
		idA    []byte
		idB    []byte
		depth  int
	}{
		{testID: "A: [11111111, 11111111] -> [10111111, 11111111]",
			idA:   []byte{255, 255},
			idB:   []byte{191, 255},
			depth: 1,
		},
		{testID: "B: [10111111, 11111111] -> [10011111, 11111111]",
			idA:   []byte{191, 255},
			idB:   []byte{159, 255},
			depth: 2,
		},
		{testID: "C: [01111111, 11111111] -> [00111111, 11111111]",
			idA:   []byte{127, 255},
			idB:   []byte{63, 255},
			depth: 1,
		},
		{testID: "D: [00000000, 11111111] -> [00000000, 01111111]",
			idA:   []byte{0, 255},
			idB:   []byte{0, 127},
			depth: 8,
		},
		{testID: "E: [01011111, 11111111] -> [01010111, 11111111]",
			idA:   []byte{95, 255},
			idB:   []byte{87, 255},
			depth: 4,
		},
		{testID: "F: [01011111, 11111111] -> [01001111, 11111111]",
			idA:   []byte{95, 255},
			idB:   []byte{79, 255},
			depth: 3,
		},
	}
	for _, c := range cases {
		testCase := c
		t.Run(testCase.testID, func(t *testing.T) {
			newID := rt.splitBucket(keyToBucketID(testCase.idA), testCase.depth)
			assert.Equal(t, testCase.idB, newID[:2])
		})
	}
}
