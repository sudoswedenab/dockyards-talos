// Copyright 2026 Sudo Sweden AB
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

package discovery

import (
	"context"
	"errors"
	"net"
	"slices"
	"time"
	"weak"

	discoveryv1 "github.com/siderolabs/discovery-api/api/v1alpha1/server/pb"
	"github.com/sudoswedenab/dockyards-talos/internal/sync"
	"google.golang.org/grpc"

	"google.golang.org/grpc/peer"
)

type ClientID string
type AffiliateID string
type ClusterID string

type ClusterAffiliateID struct {
	ClusterID   ClusterID
	AffiliateID AffiliateID
}

type ClusterAffiliate struct {
	Affiliate   *discoveryv1.Affiliate `json:"affiliate,omitempty"`
	ClusterID   ClusterID              `json:"clusterID,omitempty"`
	RemoveAfter time.Time              `json:"removeAfter,omitempty"`
}

type WatchResponse struct {
	ClusterAffiliates []ClusterAffiliate
	Deleted           bool
}

type ClusterDiscoveryServer struct {
	discoveryv1.UnimplementedClusterServer

	garbageCollectionInterval time.Duration
	killGarbageCollector      chan struct{}

	watchers          sync.MutexProtected[[]weak.Pointer[chan WatchResponse]]
	ClusterAffiliates sync.MutexProtected[map[ClusterAffiliateID]ClusterAffiliate]
}

var _ discoveryv1.ClusterServer = &ClusterDiscoveryServer{}

type ClusterDiscoveryServerOptions struct {
	StateProvider             StateProvider
	GarbageCollectionInterval time.Duration
}

type ClusterDiscoveryServerOption func(options *ClusterDiscoveryServerOptions)

func ClusterDiscoveryServerStateProvider(state StateProvider) ClusterDiscoveryServerOption {
	return func(options *ClusterDiscoveryServerOptions) {
		options.StateProvider = state
	}
}
func ClusterDiscoveryServerGarbageCollectionInterval(interval time.Duration) ClusterDiscoveryServerOption {
	return func(options *ClusterDiscoveryServerOptions) {
		options.GarbageCollectionInterval = interval
	}
}

func NewClusterDiscoveryServer(options ...ClusterDiscoveryServerOption) *ClusterDiscoveryServer {
	opts := ClusterDiscoveryServerOptions{}
	for _, option := range options {
		option(&opts)
	}

	var save SaveStateFunc
	var clusterAffiliates []ClusterAffiliate
	if opts.StateProvider != nil {
		clusterAffiliates = opts.StateProvider.Load()
		save = opts.StateProvider.Save
	}

	state := make(map[ClusterAffiliateID]ClusterAffiliate, len(clusterAffiliates))
	for _, affiliate := range clusterAffiliates {
		if affiliate.Affiliate == nil {
			continue
		}

		id := ClusterAffiliateID{
			ClusterID:   affiliate.ClusterID,
			AffiliateID: AffiliateID(affiliate.Affiliate.Id),
		}
		state[id] = affiliate
	}

	watchers := []weak.Pointer[chan WatchResponse]{}

	if save != nil {
		watch := make(chan WatchResponse, 1024)
		go statePersister(watch, clusterAffiliates, save)
		watchers = append(watchers, weak.Make(&watch))
	}

	result := &ClusterDiscoveryServer{
		garbageCollectionInterval: opts.GarbageCollectionInterval,
		killGarbageCollector:      make(chan struct{}),

		watchers:          sync.NewMutexProtected(watchers),
		ClusterAffiliates: sync.NewMutexProtected(state),
	}
	go result.garbageCollectionLoop()

	return result
}

func (s *ClusterDiscoveryServer) Close() {
	close(s.killGarbageCollector)
	s.watchers.With(func(value *[]weak.Pointer[chan WatchResponse]) {
		for _, watcher := range *value {
			w := watcher.Value()
			if w == nil {
				continue
			}
			close(*w)
		}
	})
}

func (s *ClusterDiscoveryServer) garbageCollectionLoop() {
	interval := s.garbageCollectionInterval
	if interval.Nanoseconds() == 0 {
		interval = 15 * time.Minute
	}

	ticker := time.NewTicker(interval)
	for {
		select {
		case _, ok := <-s.killGarbageCollector:
			if !ok {
				return
			}
		case <-ticker.C:
			s.collectGarbage()
		}
	}
}

func (s *ClusterDiscoveryServer) Hello(ctx context.Context, req *discoveryv1.HelloRequest) (*discoveryv1.HelloResponse, error) {
	_ = req

	p, ok := peer.FromContext(ctx)
	if !ok {
		return nil, errors.New("could not get client IP")
	}
	if p == nil {
		return nil, errors.New("could not get client IP")
	}

	addr := p.Addr
	if addr == nil {
		return nil, errors.New("could not get client IP")
	}

	clientIP := net.ParseIP(addr.String())
	if clientIP == nil {
		return nil, errors.New("could not parse client IP")
	}

	return &discoveryv1.HelloResponse{
		Redirect: nil,
		ClientIp: clientIP,
	}, nil
}

func (s *ClusterDiscoveryServer) AffiliateUpdate(ctx context.Context, req *discoveryv1.AffiliateUpdateRequest) (*discoveryv1.AffiliateUpdateResponse, error) {
	_ = ctx

	s.collectGarbage()

	// FIXME: Should we check clusterID to not be empty?
	clusterID := ClusterID(req.GetClusterId())
	affiliateID := AffiliateID(req.GetAffiliateId())
	clusterAffiliateID := ClusterAffiliateID{ClusterID: clusterID, AffiliateID: affiliateID}

	var aff ClusterAffiliate
	var createdNow bool
	s.ClusterAffiliates.With(func(a *map[ClusterAffiliateID]ClusterAffiliate) {
		affiliates := *a
		if affiliates == nil {
			affiliates = map[ClusterAffiliateID]ClusterAffiliate{}
		}

		var ok bool
		aff, ok = affiliates[clusterAffiliateID]
		if !ok {
			createdNow = true
		}

		newData := aff.Affiliate.GetData()
		// If missing, affiliate data is not updated.
		if req.GetAffiliateData() != nil {
			newData = req.GetAffiliateData()
		}

		// Endpoints are merged with the existing list of endpoints.
		endpoints := make(map[string][]byte, len(aff.Affiliate.GetEndpoints()))
		for _, endpoint := range aff.Affiliate.GetEndpoints() {
			endpoints[string(endpoint)] = endpoint
		}
		for _, endpoint := range req.GetAffiliateEndpoints() {
			endpoints[string(endpoint)] = endpoint
		}
		newEndpoints := make([][]byte, len(endpoints))[:0]
		for _, endpoint := range endpoints {
			newEndpoints = append(newEndpoints, endpoint)
		}

		ttl := req.GetTtl().AsDuration()
		if ttl.Nanoseconds() == 0 {
			ttl = 15 * time.Minute
		}
		removeAfter := time.Now().Add(ttl)

		aff.ClusterID = clusterID
		aff.RemoveAfter = removeAfter
		aff.Affiliate = &discoveryv1.Affiliate{
			Id:        string(affiliateID),
			Data:      newData,
			Endpoints: newEndpoints,
		}

		affiliates[clusterAffiliateID] = aff
		*a = affiliates
	})

	if createdNow {
		s.broadcast(WatchResponse{
			ClusterAffiliates: []ClusterAffiliate{aff},
			Deleted:           false,
		})
	}

	return nil, nil
}

func (s *ClusterDiscoveryServer) AffiliateDelete(ctx context.Context, req *discoveryv1.AffiliateDeleteRequest) (*discoveryv1.AffiliateDeleteResponse, error) {
	_ = ctx

	s.collectGarbage()

	// FIXME: Should we check clusterID to not be empty?
	clusterID := ClusterID(req.GetClusterId())
	affiliateID := AffiliateID(req.GetAffiliateId())
	clusterAffiliateID := ClusterAffiliateID{ClusterID: clusterID, AffiliateID: affiliateID}

	var affiliate ClusterAffiliate
	var affiliateFound bool

	s.ClusterAffiliates.With(func(a *map[ClusterAffiliateID]ClusterAffiliate) {
		affiliates := *a
		if affiliates == nil {
			affiliates = map[ClusterAffiliateID]ClusterAffiliate{}
		}

		affiliate, affiliateFound = affiliates[clusterAffiliateID]
		if !affiliateFound {
			return
		}

		delete(affiliates, clusterAffiliateID)
		*a = affiliates
	})
	if !affiliateFound {
		return nil, nil
	}

	s.broadcast(WatchResponse{
		ClusterAffiliates: []ClusterAffiliate{affiliate},
		Deleted:           true,
	})

	return nil, nil
}

func (s *ClusterDiscoveryServer) List(ctx context.Context, req *discoveryv1.ListRequest) (*discoveryv1.ListResponse, error) {
	_ = ctx

	s.collectGarbage()

	var affiliates []*discoveryv1.Affiliate
	s.ClusterAffiliates.With(func(ca *map[ClusterAffiliateID]ClusterAffiliate) {
		clusterAffiliates := *ca
		if clusterAffiliates == nil {
			return
		}

		count := 0
		clusterID := ClusterID(req.GetClusterId())

		for k := range clusterAffiliates {
			if k.ClusterID == clusterID {
				count++
			}
		}

		if count == 0 {
			return
		}

		affiliates = make([]*discoveryv1.Affiliate, count)[:0]
		for k, v := range clusterAffiliates {
			if k.ClusterID != clusterID {
				continue
			}
			if v.ClusterID != clusterID {
				// Make double sure we don't include affiliate data from other clusters
				continue
			}
			affiliates = append(affiliates, v.Affiliate)
		}
	})

	return &discoveryv1.ListResponse{
		Affiliates: affiliates,
	}, nil
}

func (s *ClusterDiscoveryServer) Watch(req *discoveryv1.WatchRequest, res grpc.ServerStreamingServer[discoveryv1.WatchResponse]) error {
	ch := make(chan WatchResponse, 1024)

	s.watchers.With(func(watchers *[]weak.Pointer[chan WatchResponse]) {
		*watchers = append(*watchers, weak.Make(&ch))
	})

	s.collectGarbage()

	for msg := range ch {
		count := clusterAffiliateCount(msg.ClusterAffiliates, ClusterID(req.ClusterId))
		if count == 0 {
			continue
		}
		affiliates := make([]*discoveryv1.Affiliate, count)[:0]
		for _, clusterAffiliate := range msg.ClusterAffiliates {
			if clusterAffiliate.ClusterID != ClusterID(req.GetClusterId()) {
				continue
			}
			if clusterAffiliate.Affiliate == nil {
				continue
			}
			affiliates = append(affiliates, clusterAffiliate.Affiliate)
		}

		if msg.Deleted {
			// If deleted, we only provide the IDs
			affiliates = stripAffiliateContent(affiliates)
		}
		err := res.Send(&discoveryv1.WatchResponse{
			Affiliates: affiliates,
			Deleted:    msg.Deleted,
		})
		if err != nil {
			return err
		}
	}

	return nil
}

func (s *ClusterDiscoveryServer) broadcast(message WatchResponse) {
	s.watchers.With(func(watchers *[]weak.Pointer[chan WatchResponse]) {
		for _, watcher := range *watchers {
			w := watcher.Value()
			if w == nil {
				continue
			}

			*w <- message
		}
	})
}

func (s *ClusterDiscoveryServer) collectGarbage() {
	now := time.Now()

	var deleted []ClusterAffiliate
	s.ClusterAffiliates.With(func(a *map[ClusterAffiliateID]ClusterAffiliate) {
		affiliates := *a
		if affiliates == nil {
			affiliates = map[ClusterAffiliateID]ClusterAffiliate{}
		}

		deleted = make([]ClusterAffiliate, len(affiliates))[:0]
		deletedIDs := make([]ClusterAffiliateID, len(affiliates))[:0]
		for id, affiliate := range affiliates {
			if now.After(affiliate.RemoveAfter) {
				deleted = append(deleted, affiliate)
				deletedIDs = append(deletedIDs, id)
			}
		}
		for _, deleted := range deletedIDs {
			delete(affiliates, deleted)
		}

		*a = affiliates
	})

	if len(deleted) != 0 {
		s.broadcast(WatchResponse{
			ClusterAffiliates: deleted,
			Deleted:           true,
		})
	}

	s.watchers.With(func(watchers *[]weak.Pointer[chan WatchResponse]) {
		newWatchers := make([]weak.Pointer[chan WatchResponse], len(*watchers))[:0]
		for _, watcher := range *watchers {
			if watcher.Value() == nil {
				continue
			}
			newWatchers = append(newWatchers, watcher)
		}

		if len(*watchers) == len(newWatchers) {
			return // The array did not change
		}
		*watchers = newWatchers
	})
}

func statePersister(watch chan WatchResponse, initialState []ClusterAffiliate, save SaveStateFunc) {
	affiliates := slices.Clone(initialState)

	state := make(map[ClusterAffiliateID]ClusterAffiliate, len(affiliates))
	for _, affiliate := range affiliates {
		ca := ClusterAffiliateID{
			ClusterID:   affiliate.ClusterID,
			AffiliateID: AffiliateID(affiliate.Affiliate.GetId()),
		}
		if ca.AffiliateID == "" {
			continue
		}
		state[ca] = affiliate
	}

	for request := range watch {
		if request.Deleted {
			for _, affiliate := range request.ClusterAffiliates {
				ca := ClusterAffiliateID{
					ClusterID:   affiliate.ClusterID,
					AffiliateID: AffiliateID(affiliate.Affiliate.GetId()),
				}
				if ca.AffiliateID == "" {
					continue
				}
				delete(state, ca)
			}
		} else {
			for _, affiliate := range request.ClusterAffiliates {
				ca := ClusterAffiliateID{
					ClusterID:   affiliate.ClusterID,
					AffiliateID: AffiliateID(affiliate.Affiliate.GetId()),
				}
				if ca.AffiliateID == "" {
					continue
				}
				state[ca] = affiliate
			}
		}

		affiliates = affiliates[:0]

		for _, affiliate := range state {
			affiliates = append(affiliates, affiliate)
		}

		save(affiliates)
	}
}

func stripAffiliateContent(affiliates []*discoveryv1.Affiliate) []*discoveryv1.Affiliate {
	result := make([]*discoveryv1.Affiliate, len(affiliates))[:0]
	for _, affiliate := range affiliates {
		if affiliate == nil {
			continue
		}
		result = append(result, &discoveryv1.Affiliate{
			Id: affiliate.Id,
		})
	}

	return result
}

func clusterAffiliateCount(ca []ClusterAffiliate, clusterID ClusterID) int {
	var count int

	for _, ca := range ca {
		if ca.ClusterID == clusterID {
			count++
		}
	}

	return count
}
