// Copyright (C) 2019 Storj Labs, Inc.
// See LICENSE for copying information.

package audit

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"time"

	"github.com/vivint/infectious"
	"github.com/zeebo/errs"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	monkit "gopkg.in/spacemonkeygo/monkit.v2"

	"storj.io/storj/internal/memory"
	"storj.io/storj/pkg/auth/signing"
	"storj.io/storj/pkg/identity"
	"storj.io/storj/pkg/overlay"
	"storj.io/storj/pkg/pb"
	"storj.io/storj/pkg/pkcrypto"
	"storj.io/storj/pkg/storj"
	"storj.io/storj/pkg/transport"
	"storj.io/storj/satellite/orders"
	"storj.io/storj/uplink/piecestore"
)

var (
	mon = monkit.Package()

	// ErrNotEnoughShares is the errs class for when not enough shares are available to do an audit
	ErrNotEnoughShares = errs.Class("not enough shares for successful audit")
)

// Share represents required information about an audited share
type Share struct {
	Error    error
	PieceNum int
	Data     []byte
}

// Verifier helps verify the correctness of a given stripe
type Verifier struct {
	log               *zap.Logger
	orders            *orders.Service
	auditor           *identity.PeerIdentity
	transport         transport.Client
	overlay           *overlay.Cache
	containment       Containment
	reporter          reporter
	minBytesPerSecond memory.Size
}

// NewVerifier creates a Verifier
func NewVerifier(log *zap.Logger, reporter reporter, transport transport.Client, overlay *overlay.Cache, containment Containment, orders *orders.Service, id *identity.FullIdentity, minBytesPerSecond memory.Size) *Verifier {
	return &Verifier{
		log:               log,
		reporter:          reporter,
		orders:            orders,
		auditor:           id.PeerIdentity(),
		transport:         transport,
		overlay:           overlay,
		containment:       containment,
		minBytesPerSecond: minBytesPerSecond,
	}
}

// Verify downloads shares then verifies the data correctness at the given stripe
func (verifier *Verifier) Verify(ctx context.Context, stripe *Stripe, skip map[storj.NodeID]bool) (report *Report, err error) {
	defer mon.Task()(&ctx)(&err)

	pointer := stripe.Segment
	shareSize := pointer.GetRemote().GetRedundancy().GetErasureShareSize()
	bucketID := createBucketID(stripe.SegmentPath)

	// TODO(kaloyan): CreateAuditOrderLimits checks overlay cache if nodes are online and won't return
	// order limits for offline node. So we miss to record them as offline during the audit
	orderLimits, err := verifier.orders.CreateAuditOrderLimits(ctx, verifier.auditor, bucketID, pointer, skip)
	if err != nil {
		return nil, err
	}

	shares, nodes, err := verifier.DownloadShares(ctx, orderLimits, stripe.Index, shareSize)
	if err != nil {
		return nil, err
	}

	var offlineNodes storj.NodeIDList
	var failedNodes storj.NodeIDList
	containedNodes := make(map[int]storj.NodeID)
	sharesToAudit := make(map[int]Share)

	for pieceNum, share := range shares {
		if share.Error != nil {
			// TODO(kaloyan): we need to check the logic here if we correctly identify offline nodes from those that didn't respond.
			if share.Error == context.DeadlineExceeded || !transport.Error.Has(share.Error) || ContainError.Has(share.Error) {
				containedNodes[pieceNum] = nodes[pieceNum]
			} else {
				offlineNodes = append(offlineNodes, nodes[pieceNum])
			}
		} else {
			sharesToAudit[pieceNum] = share
		}
	}

	required := int(pointer.Remote.Redundancy.GetMinReq())
	total := int(pointer.Remote.Redundancy.GetTotal())

	if len(sharesToAudit) < required {
		return &Report{
			Offlines: offlineNodes,
		}, ErrNotEnoughShares.New("got %d, required %d", len(sharesToAudit), required)
	}

	pieceNums, correctedShares, err := auditShares(ctx, required, total, sharesToAudit)
	if err != nil {
		return &Report{
			Offlines: offlineNodes,
		}, err
	}

	for _, pieceNum := range pieceNums {
		failedNodes = append(failedNodes, nodes[pieceNum])
	}

	successNodes := getSuccessNodes(ctx, nodes, failedNodes, offlineNodes, containedNodes)

	pendingAudits, err := createPendingAudits(containedNodes, correctedShares, stripe)
	if err != nil {
		return &Report{
			Successes: successNodes,
			Fails:     failedNodes,
			Offlines:  offlineNodes,
		}, err
	}

	return &Report{
		Successes:     successNodes,
		Fails:         failedNodes,
		Offlines:      offlineNodes,
		PendingAudits: pendingAudits,
	}, nil
}

// DownloadShares downloads shares from the nodes where remote pieces are located
func (verifier *Verifier) DownloadShares(ctx context.Context, limits []*pb.AddressedOrderLimit, stripeIndex int64, shareSize int32) (shares map[int]Share, nodes map[int]storj.NodeID, err error) {
	defer mon.Task()(&ctx)(&err)

	shares = make(map[int]Share, len(limits))
	nodes = make(map[int]storj.NodeID, len(limits))

	for i, limit := range limits {
		if limit == nil {
			continue
		}

		// TODO(kaloyan): execute in goroutine for better performance
		share, err := verifier.GetShare(ctx, limit, stripeIndex, shareSize, i)
		if err != nil {
			share = Share{
				Error:    err,
				PieceNum: i,
				Data:     nil,
			}
		}

		shares[share.PieceNum] = share
		nodes[share.PieceNum] = limit.GetLimit().StorageNodeId
	}

	return shares, nodes, nil
}

// Reverify reverifies the contained nodes in the stripe
func (verifier *Verifier) Reverify(ctx context.Context, stripe *Stripe) (report *Report, err error) {
	defer mon.Task()(&ctx)(&err)

	// result status enum
	const (
		skipped = iota
		success
		offline
		failed
		contained
		erred
	)

	type result struct {
		nodeID       storj.NodeID
		status       int
		pendingAudit *PendingAudit
		err          error
	}

	pieces := stripe.Segment.GetRemote().GetRemotePieces()
	ch := make(chan result, len(pieces))

	for _, piece := range pieces {
		pending, err := verifier.containment.Get(ctx, piece.NodeId)
		if err != nil {
			if ErrContainedNotFound.Has(err) {
				ch <- result{nodeID: piece.NodeId, status: skipped}
				continue
			}
			ch <- result{nodeID: piece.NodeId, status: erred, err: err}
			continue
		}

		go func(pending *PendingAudit, piece *pb.RemotePiece) {
			limit, err := verifier.orders.CreateAuditOrderLimit(ctx, verifier.auditor, createBucketID(stripe.SegmentPath), pending.NodeID, pending.PieceID, pending.ShareSize)
			if err != nil {
				if overlay.ErrNodeOffline.Has(err) {
					ch <- result{nodeID: piece.NodeId, status: offline}
					return
				}
				ch <- result{nodeID: piece.NodeId, status: erred, err: err}
				return
			}

			share, err := verifier.GetShare(ctx, limit, pending.StripeIndex, pending.ShareSize, int(piece.PieceNum))
			if err != nil {
				// TODO(kaloyan): we need to check the logic here if we correctly identify offline nodes from those that didn't respond.
				if err == context.DeadlineExceeded || !transport.Error.Has(err) {
					ch <- result{nodeID: piece.NodeId, status: contained}
				} else {
					ch <- result{nodeID: piece.NodeId, status: offline}
				}
				return
			}

			downloadedHash := pkcrypto.SHA256Hash(share.Data)
			if bytes.Equal(downloadedHash, pending.ExpectedShareHash) {
				ch <- result{nodeID: piece.NodeId, status: success}
			} else {
				ch <- result{nodeID: piece.NodeId, status: failed}
			}
		}(pending, piece)
	}

	report = &Report{}
	for range pieces {
		result := <-ch
		switch result.status {
		case success:
			report.Successes = append(report.Successes, result.nodeID)
		case offline:
			report.Offlines = append(report.Offlines, result.nodeID)
		case failed:
			report.Fails = append(report.Fails, result.nodeID)
		case contained:
			report.PendingAudits = append(report.PendingAudits, result.pendingAudit)
		case erred:
			err = errs.Combine(err, result.err)
		}
	}

	return report, err
}

// GetShare use piece store client to download shares from nodes
func (verifier *Verifier) GetShare(ctx context.Context, limit *pb.AddressedOrderLimit, stripeIndex int64, shareSize int32, pieceNum int) (share Share, err error) {
	defer mon.Task()(&ctx)(&err)

	bandwidthMsgSize := shareSize

	// determines number of seconds allotted for receiving data from a storage node
	timedCtx := ctx
	if verifier.minBytesPerSecond > 0 {
		maxTransferTime := time.Duration(int64(time.Second) * int64(bandwidthMsgSize) / verifier.minBytesPerSecond.Int64())
		if maxTransferTime < (5 * time.Second) {
			maxTransferTime = 5 * time.Second
		}
		var cancel func()
		timedCtx, cancel = context.WithTimeout(ctx, maxTransferTime)
		defer cancel()
	}

	storageNodeID := limit.GetLimit().StorageNodeId

	conn, err := verifier.transport.DialNode(timedCtx, &pb.Node{
		Id:      storageNodeID,
		Address: limit.GetStorageNodeAddress(),
	})
	if err != nil {
		return Share{}, err
	}
	ps := piecestore.NewClient(
		verifier.log.Named(storageNodeID.String()),
		signing.SignerFromFullIdentity(verifier.transport.Identity()),
		conn,
		piecestore.DefaultConfig,
	)
	defer func() {
		err := ps.Close()
		if err != nil {
			verifier.log.Error("audit verifier failed to close conn to node: %+v", zap.Error(err))
		}
	}()

	offset := int64(shareSize) * stripeIndex

	downloader, err := ps.Download(timedCtx, limit.GetLimit(), offset, int64(shareSize))
	if err != nil {
		return Share{}, err
	}
	defer func() { err = errs.Combine(err, downloader.Close()) }()

	buf := make([]byte, shareSize)
	_, err = io.ReadFull(downloader, buf)
	if err != nil {
		return Share{}, err
	}

	return Share{
		Error:    nil,
		PieceNum: pieceNum,
		Data:     buf,
	}, nil
}

var (
	errStorageNodeOffline        = errors.New("Storage Node is offline")
	errStorageNodeDialTimeout    = errors.New("Storage Node dialing timed out")
	errStorageNodeDialUnexpected = errors.New("Storage Node returned an unexpected error when dialing")
)

// TODO: WIP#if/v3-1760 write docs
func (verifier *Verifier) getNodeConnection(id storj.NodeID, address *pb.NodeAddress) (*grpc.ClientConn, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", address.GetAddress())
	if err != nil {
		if err == context.DeadlineExceeded {
			return nil, errStorageNodeDialTimeout
		}

		if strings.Contains(err.Error(), "connect: connection refused") {
			return nil, errStorageNodeOffline
		}

		return nil, errStorageNodeDialUnexpected
	}

	if err := conn.Close(); err != nil {
		// TODO: WIP#if/v3-1760 Do we want to return this error? it doesn't look
		// important for what this method tries to achieve
		verifier.log.Warn(
			"Node dialing test connection failed on close",
			zap.Error(errs.Wrap(err)),
		)
	}

	ctx, cancel = context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	grpcConn, err := verifier.transport.DialNode(ctx, &pb.Node{
		Id:      id,
		Address: address,
	})
	if err != nil {
		// TODO: WIP#if/v3-1760 we could check here err == context.DeadlineExceeded
		// but without the previous Dial almost all the cases detected by it fall
		// under that condition so we cannot discern between those
		return nil, errStorageNodeDialUnexpected
	}

	return grpcConn, nil
}

// auditShares takes the downloaded shares and uses infectious's Correct function to check that they
// haven't been altered. auditShares returns a slice containing the piece numbers of altered shares,
// and a slice of the corrected shares.
func auditShares(ctx context.Context, required, total int, originals map[int]Share) (pieceNums []int, corrected []infectious.Share, err error) {
	defer mon.Task()(&ctx)(&err)
	f, err := infectious.NewFEC(required, total)
	if err != nil {
		return nil, nil, err
	}

	copies, err := makeCopies(ctx, originals)
	if err != nil {
		return nil, nil, err
	}

	err = f.Correct(copies)
	if err != nil {
		return nil, nil, err
	}

	for _, share := range copies {
		if !bytes.Equal(originals[share.Number].Data, share.Data) {
			pieceNums = append(pieceNums, share.Number)
		}
	}
	return pieceNums, copies, nil
}

// makeCopies takes in a map of audit Shares and deep copies their data to a slice of infectious Shares
func makeCopies(ctx context.Context, originals map[int]Share) (copies []infectious.Share, err error) {
	defer mon.Task()(&ctx)(&err)
	copies = make([]infectious.Share, 0, len(originals))
	for _, original := range originals {
		copies = append(copies, infectious.Share{
			Data:   append([]byte{}, original.Data...),
			Number: original.PieceNum})
	}
	return copies, nil
}

// getSuccessNodes uses the failed nodes, offline nodes and contained nodes arrays to determine which nodes passed the audit
func getSuccessNodes(ctx context.Context, nodes map[int]storj.NodeID, failedNodes, offlineNodes storj.NodeIDList, containedNodes map[int]storj.NodeID) (successNodes storj.NodeIDList) {
	fails := make(map[storj.NodeID]bool)
	for _, fail := range failedNodes {
		fails[fail] = true
	}
	for _, offline := range offlineNodes {
		fails[offline] = true
	}
	for _, contained := range containedNodes {
		fails[contained] = true
	}

	for _, node := range nodes {
		if !fails[node] {
			successNodes = append(successNodes, node)
		}
	}

	return successNodes
}

func createBucketID(path storj.Path) []byte {
	comps := storj.SplitPath(path)
	if len(comps) < 3 {
		return nil
	}
	// project_id/bucket_name
	return []byte(storj.JoinPaths(comps[0], comps[2]))
}

func createPendingAudits(containedNodes map[int]storj.NodeID, correctedShares []infectious.Share, stripe *Stripe) ([]*PendingAudit, error) {
	if len(containedNodes) > 0 {
		return nil, nil
	}

	redundancy := stripe.Segment.GetRemote().GetRedundancy()
	required := int(redundancy.GetMinReq())
	total := int(redundancy.GetTotal())
	shareSize := redundancy.GetErasureShareSize()

	fec, err := infectious.NewFEC(required, total)
	if err != nil {
		return nil, Error.Wrap(err)
	}

	stripeData, err := rebuildStripe(fec, correctedShares, int(shareSize))
	if err != nil {
		return nil, Error.Wrap(err)
	}

	var pendingAudits []*PendingAudit
	for pieceNum, nodeID := range containedNodes {
		share := make([]byte, shareSize)
		err = fec.EncodeSingle(stripeData, share, pieceNum)
		if err != nil {
			return nil, Error.Wrap(err)
		}
		pendingAudits = append(pendingAudits, &PendingAudit{
			NodeID:            nodeID,
			PieceID:           stripe.Segment.GetRemote().RootPieceId,
			StripeIndex:       stripe.Index,
			ShareSize:         shareSize,
			ExpectedShareHash: pkcrypto.SHA256Hash(share),
		})
	}

	return pendingAudits, nil
}

func rebuildStripe(fec *infectious.FEC, corrected []infectious.Share, shareSize int) ([]byte, error) {
	stripe := make([]byte, fec.Required()*shareSize)
	err := fec.Rebuild(corrected, func(share infectious.Share) {
		copy(stripe[share.Number*shareSize:], share.Data)
	})
	if err != nil {
		return nil, err
	}
	return stripe, nil
}
