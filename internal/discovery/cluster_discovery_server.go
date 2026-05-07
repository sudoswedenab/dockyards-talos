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
	"fmt"
	"log/slog"
	"net"
	"slices"
	"time"
	"weak"

	discoveryv1 "github.com/siderolabs/discovery-api/api/v1alpha1/server/pb"
	"github.com/sudoswedenab/dockyards-talos/internal/sync"
	"google.golang.org/grpc"

	"google.golang.org/grpc/peer"
)

type (
	ClientID    string
	AffiliateID string
	ClusterID   string
)

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

	Logger *slog.Logger

	garbageCollectionInterval time.Duration
	killGarbageCollector      chan struct{}

	watchers          sync.MutexProtected[[]weak.Pointer[chan WatchResponse]]
	ClusterAffiliates sync.MutexProtected[map[ClusterAffiliateID]ClusterAffiliate]
}

var _ discoveryv1.ClusterServer = &ClusterDiscoveryServer{}

type ClusterDiscoveryServerOptions struct {
	Logger                    *slog.Logger
	StateProvider             StateProvider
	GarbageCollectionInterval time.Duration
}

type ClusterDiscoveryServerOption func(options *ClusterDiscoveryServerOptions)

func ClusterDiscoveryServerLogger(logger *slog.Logger) ClusterDiscoveryServerOption {
	return func(options *ClusterDiscoveryServerOptions) {
		options.Logger = logger
	}
}

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
		Logger: opts.Logger,

		garbageCollectionInterval: opts.GarbageCollectionInterval,
		killGarbageCollector:      make(chan struct{}),

		watchers:          sync.NewMutexProtected(watchers),
		ClusterAffiliates: sync.NewMutexProtected(state),
	}
	if result.Logger != nil {
		result.Logger.Debug("cluster discovery server created",
			"initialAffiliates", len(state),
			"gcInterval", result.garbageCollectionInterval,
		)
	}
	go result.garbageCollectionLoop()

	return result
}

func (s *ClusterDiscoveryServer) Close() {
	if s.Logger != nil {
		s.Logger.Debug("cluster discovery server closing")
	}
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
	if s.Logger != nil {
		s.Logger.Debug("starting garbage collection loop", "interval", interval)
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
	if s.Logger != nil {
		s.Logger.Debug("hello request")
	}

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

	if s.Logger != nil {
		s.Logger.Debug("hello peer addr",
			"addr", addr.String(),
			"network", addr.Network(),
			"type", fmt.Sprintf("%T", addr),
		)
	}

	var clientIP net.IP
	switch a := addr.(type) {
	case *net.TCPAddr:
		clientIP = a.IP
	case *net.UDPAddr:
		clientIP = a.IP
	case *net.IPAddr:
		clientIP = a.IP
	default:
		// Often formatted as "ip:port"; handle both that and bare IP.
		if host, _, err := net.SplitHostPort(addr.String()); err == nil {
			clientIP = net.ParseIP(host)
		} else {
			clientIP = net.ParseIP(addr.String())
		}
	}
	if clientIP == nil {
		if s.Logger != nil {
			s.Logger.Debug("could not parse client IP",
				"addr", addr.String(),
				"network", addr.Network(),
				"type", fmt.Sprintf("%T", addr),
			)
		}

		return nil, errors.New("could not parse client IP")
	}

	if s.Logger != nil {
		s.Logger.Debug("hello response", "clientIP", clientIP.String())
	}

	return &discoveryv1.HelloResponse{
		Redirect: nil,
		ClientIp: clientIP,
	}, nil
}

func (s *ClusterDiscoveryServer) AffiliateUpdate(ctx context.Context, req *discoveryv1.AffiliateUpdateRequest) (*discoveryv1.AffiliateUpdateResponse, error) {
	if s.Logger != nil {
		if p, ok := peer.FromContext(ctx); ok && p != nil {
			s.Logger.Debug("affiliate update request",
				"clusterID", req.GetClusterId(),
				"affiliateID", req.GetAffiliateId(),
				"peer", p.Addr,
			)
		} else {
			s.Logger.Debug("affiliate update request",
				"clusterID", req.GetClusterId(),
				"affiliateID", req.GetAffiliateId(),
			)
		}
	}

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
		if s.Logger != nil {
			s.Logger.Debug("affiliate created",
				"clusterID", clusterID,
				"affiliateID", affiliateID,
				"removeAfter", aff.RemoveAfter,
				"endpoints", len(aff.Affiliate.GetEndpoints()),
			)
		}
		s.broadcast(WatchResponse{
			ClusterAffiliates: []ClusterAffiliate{aff},
			Deleted:           false,
		})
	} else if s.Logger != nil {
		s.Logger.Debug("affiliate updated",
			"clusterID", clusterID,
			"affiliateID", affiliateID,
			"removeAfter", aff.RemoveAfter,
			"endpoints", len(aff.Affiliate.GetEndpoints()),
		)
	}

	return nil, nil
}

func (s *ClusterDiscoveryServer) AffiliateDelete(ctx context.Context, req *discoveryv1.AffiliateDeleteRequest) (*discoveryv1.AffiliateDeleteResponse, error) {
	if s.Logger != nil {
		if p, ok := peer.FromContext(ctx); ok && p != nil {
			s.Logger.Debug("affiliate delete request",
				"clusterID", req.GetClusterId(),
				"affiliateID", req.GetAffiliateId(),
				"peer", p.Addr,
			)
		} else {
			s.Logger.Debug("affiliate delete request",
				"clusterID", req.GetClusterId(),
				"affiliateID", req.GetAffiliateId(),
			)
		}
	}

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
		if s.Logger != nil {
			s.Logger.Debug("affiliate delete: not found", "clusterID", clusterID, "affiliateID", affiliateID)
		}

		return nil, nil
	}
	if s.Logger != nil {
		s.Logger.Debug("affiliate deleted", "clusterID", clusterID, "affiliateID", affiliateID)
	}

	s.broadcast(WatchResponse{
		ClusterAffiliates: []ClusterAffiliate{affiliate},
		Deleted:           true,
	})

	return nil, nil
}

func (s *ClusterDiscoveryServer) List(ctx context.Context, req *discoveryv1.ListRequest) (*discoveryv1.ListResponse, error) {
	if s.Logger != nil {
		if p, ok := peer.FromContext(ctx); ok && p != nil {
			s.Logger.Debug("list request", "clusterID", req.GetClusterId(), "peer", p.Addr)
		} else {
			s.Logger.Debug("list request", "clusterID", req.GetClusterId())
		}
	}

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

	resp := &discoveryv1.ListResponse{
		Affiliates: affiliates,
	}
	if s.Logger != nil {
		s.Logger.Debug("list response", "clusterID", req.GetClusterId(), "affiliates", len(resp.GetAffiliates()))
	}

	return resp, nil
}

func (s *ClusterDiscoveryServer) Watch(req *discoveryv1.WatchRequest, res grpc.ServerStreamingServer[discoveryv1.WatchResponse]) error {
	ch := make(chan WatchResponse, 1024)
	if s.Logger != nil {
		s.Logger.Debug("watch started", "clusterID", req.GetClusterId())
	}

	s.watchers.With(func(watchers *[]weak.Pointer[chan WatchResponse]) {
		*watchers = append(*watchers, weak.Make(&ch))
	})

	s.collectGarbage()

	for {
		select {
		case <-res.Context().Done():
			if s.Logger != nil {
				s.Logger.Debug("watch ended", "clusterID", req.GetClusterId(), "err", res.Context().Err())
			}

			return res.Context().Err()
		case msg, ok := <-ch:
			if !ok {
				if s.Logger != nil {
					s.Logger.Debug("watch channel closed", "clusterID", req.GetClusterId())
				}

				return nil
			}

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
				if s.Logger != nil {
					s.Logger.Debug("watch send failed", "clusterID", req.GetClusterId(), "err", err)
				}

				return err
			}
		}
	}
}

func (s *ClusterDiscoveryServer) broadcast(message WatchResponse) {
	if s.Logger != nil {
		s.Logger.Debug("broadcasting update", "affiliates", len(message.ClusterAffiliates), "deleted", message.Deleted)
	}
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
		if s.Logger != nil {
			s.Logger.Debug("garbage collected affiliates", "count", len(deleted))
		}
		s.broadcast(WatchResponse{
			ClusterAffiliates: deleted,
			Deleted:           true,
		})
	}

	s.watchers.With(func(watchers *[]weak.Pointer[chan WatchResponse]) {
		before := len(*watchers)
		newWatchers := make([]weak.Pointer[chan WatchResponse], len(*watchers))[:0]
		for _, watcher := range *watchers {
			if watcher.Value() == nil {
				continue
			}
			newWatchers = append(newWatchers, watcher)
		}

		if before == len(newWatchers) {
			return // The array did not change
		}
		*watchers = newWatchers
		if s.Logger != nil {
			s.Logger.Debug("cleaned up watchers", "before", before, "after", len(newWatchers))
		}
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
