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

package discovery_test

import (
	"math"
	"net"
	"slices"
	"testing"
	"time"

	discoveryv1 "github.com/siderolabs/discovery-api/api/v1alpha1/server/pb"
	"github.com/sudoswedenab/dockyards-talos/internal/discovery"
	"github.com/sudoswedenab/dockyards-talos/internal/sync"

	"google.golang.org/grpc/peer"
	"google.golang.org/protobuf/types/known/durationpb"
)

func TestHello(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		server := discovery.ClusterDiscoveryServer{}

		addr := net.IPv4(127, 1, 1, 1)

		ctx := peer.NewContext(t.Context(), &peer.Peer{
			Addr: &net.IPAddr{IP: addr},
		})

		res, err := server.Hello(ctx, &discoveryv1.HelloRequest{
			ClusterId: "some-cluster",
			ClientVersion: "1",
		})
		if err != nil {
			t.Errorf("expected success when providing an IP, but got: %s", err)
			return
		}

		if res == nil {
			t.Error("expected response to not be nil")
			return
		}

		if res.ClientIp == nil {
			t.Error("expected client IP to not be nil")
			return
		}

		if !slices.Equal(res.ClientIp, addr) {
			t.Errorf("expected client IP to be %s, but found %s", addr.String(), net.IP(res.ClientIp).String())
			return
		}
	})

	t.Run("nil IP", func(t *testing.T) {
		server := discovery.ClusterDiscoveryServer{}

		ctx := peer.NewContext(t.Context(), &peer.Peer{
			Addr: &net.IPAddr{IP: nil},
		})
		_, err := server.Hello(ctx, &discoveryv1.HelloRequest{})
		if err == nil {
			t.Errorf("expected hello to respond with error")
			return
		}
	})

	t.Run("invalid address", func(t *testing.T) {
		server := discovery.ClusterDiscoveryServer{}

		ctx := peer.NewContext(t.Context(), &peer.Peer{
			Addr: nil,
		})
		_, err := server.Hello(ctx, &discoveryv1.HelloRequest{})
		if err == nil {
			t.Errorf("expected hello to respond with error")
			return
		}
	})
}

func TestAffiliateUpdate(t *testing.T) {
	t.Run("nil request does not crash", func(t *testing.T) {
		server := discovery.ClusterDiscoveryServer{}

		_, err := server.AffiliateUpdate(t.Context(), nil)
		if err != nil {
			t.Error(err)
		}
	})

	t.Run("empty request does not crash", func(t *testing.T) {
		server := discovery.ClusterDiscoveryServer{}

		_, err := server.AffiliateUpdate(t.Context(), &discoveryv1.AffiliateUpdateRequest{})
		if err != nil {
			t.Error(err)
		}
	})
}

func TestAffiliateDelete(t *testing.T) {
	t.Run("nil request does not crash", func(t *testing.T) {
		server := discovery.ClusterDiscoveryServer{}

		_, err := server.AffiliateDelete(t.Context(), nil)
		if err != nil {
			t.Error(err)
		}
	})

	t.Run("empty request does not crash", func(t *testing.T) {
		server := discovery.ClusterDiscoveryServer{}

		_, err := server.AffiliateDelete(t.Context(), &discoveryv1.AffiliateDeleteRequest{})
		if err != nil {
			t.Error(err)
		}
	})
}

func TestList(t *testing.T) {
	t.Run("nil request does not crash", func(t *testing.T) {
		server := discovery.ClusterDiscoveryServer{}

		_, err := server.List(t.Context(), nil)
		if err != nil {
			t.Error(err)
		}
	})

	t.Run("empty request does not crash", func(t *testing.T) {
		server := discovery.ClusterDiscoveryServer{}

		_, err := server.List(t.Context(), &discoveryv1.ListRequest{})
		if err != nil {
			t.Error(err)
		}
	})

	t.Run("only lists selected cluster affiliates", func(t *testing.T) {
		distantFuture := time.Now().Add(time.Duration(math.MaxInt64))

		server := discovery.ClusterDiscoveryServer{
			ClusterAffiliates: sync.NewMutexProtected(map[discovery.ClusterAffiliateID]discovery.ClusterAffiliate{
				ca("some-cluster", "some-affiliate"): discovery.ClusterAffiliate{
					ClusterID: discovery.ClusterID("some-cluster"),
					Affiliate: nil,
					RemoveAfter: distantFuture,
				},
				ca("some-other-cluster", "some-affiliate"): discovery.ClusterAffiliate{
					ClusterID: discovery.ClusterID("some-other-cluster"),
					Affiliate: nil,
					RemoveAfter: distantFuture,
				},
			}),
		}

		res, err := server.List(t.Context(), &discoveryv1.ListRequest{
			ClusterId: "some-cluster",
		})
		if err != nil {
			t.Error(err)
		}

		if len(res.GetAffiliates()) != 1 {
			t.Errorf("expected 1 affiliate, but found %d", len(res.GetAffiliates()))
			return
		}
	})

	t.Run("list on unknown cluster returns nothing", func(t *testing.T) {
		distantFuture := time.Now().Add(time.Duration(math.MaxInt64))

		server := discovery.ClusterDiscoveryServer{
			ClusterAffiliates: sync.NewMutexProtected(map[discovery.ClusterAffiliateID]discovery.ClusterAffiliate{
				ca("some-cluster", "some-affiliate"): discovery.ClusterAffiliate{
					ClusterID: discovery.ClusterID("some-cluster"),
					Affiliate: nil,
					RemoveAfter: distantFuture,
				},
				ca("some-other-cluster", "some-affiliate"): discovery.ClusterAffiliate{
					ClusterID: discovery.ClusterID("some-other-cluster"),
					Affiliate: nil,
					RemoveAfter: distantFuture,
				},
			}),
		}

		res, err := server.List(t.Context(), &discoveryv1.ListRequest{
			ClusterId: "some-unknown-cluster",
		})
		if err != nil {
			t.Error(err)
		}

		if len(res.GetAffiliates()) != 0 {
			t.Errorf("expected 0 affiliate, but found %d", len(res.GetAffiliates()))
			return
		}
	})

	t.Run("new affiliates are added", func(t *testing.T) {
		distantFuture := time.Now().Add(time.Duration(math.MaxInt64))

		server := discovery.ClusterDiscoveryServer{
			ClusterAffiliates: sync.NewMutexProtected(map[discovery.ClusterAffiliateID]discovery.ClusterAffiliate{
				ca("some-cluster", "some-affiliate"): discovery.ClusterAffiliate{
					ClusterID: discovery.ClusterID("some-cluster"),
					Affiliate: nil,
					RemoveAfter: distantFuture,
				},
				ca("some-other-cluster", "some-affiliate"): discovery.ClusterAffiliate{
					ClusterID: discovery.ClusterID("some-other-cluster"),
					Affiliate: nil,
					RemoveAfter: distantFuture,
				},
			}),
		}

		res, err := server.List(t.Context(), &discoveryv1.ListRequest{
			ClusterId: "some-third-cluster",
		})
		if err != nil {
			t.Error(err)
		}

		if len(res.GetAffiliates()) != 0 {
			t.Errorf("expected 0 affiliate, but found %d", len(res.GetAffiliates()))
			return
		}

		_, err = server.AffiliateUpdate(t.Context(), &discoveryv1.AffiliateUpdateRequest{
			ClusterId: "some-third-cluster",
			AffiliateId: "some-affiliate",
			AffiliateData: nil,
			AffiliateEndpoints: nil,
			Ttl: durationpb.New(math.MaxInt64),
		})
		if err != nil {
			t.Error(err)
		}

		res, err = server.List(t.Context(), &discoveryv1.ListRequest{
			ClusterId: "some-third-cluster",
		})
		if err != nil {
			t.Error(err)
		}

		if len(res.GetAffiliates()) != 1 {
			t.Errorf("expected 1 affiliate, but found %d", len(res.GetAffiliates()))
			return
		}
	})

	t.Run("garbage is collected before responding", func(t *testing.T) {
		distantPast := time.Now().Add(-time.Duration(math.MaxInt64))

		server := discovery.ClusterDiscoveryServer{
			ClusterAffiliates: sync.NewMutexProtected(map[discovery.ClusterAffiliateID]discovery.ClusterAffiliate{
				ca("some-cluster", "some-affiliate"): discovery.ClusterAffiliate{
					ClusterID: discovery.ClusterID("some-cluster"),
					Affiliate: nil,
					RemoveAfter: distantPast,
				},
			}),
		}

		res, err := server.List(t.Context(), &discoveryv1.ListRequest{
			ClusterId: "some-cluster",
		})
		if err != nil {
			t.Error(err)
		}

		if len(res.GetAffiliates()) != 0 {
			t.Errorf("expected 0 affiliate, but found %d", len(res.GetAffiliates()))
			return
		}
	})
}

func ca(cluster string, affiliate string) discovery.ClusterAffiliateID {
	return discovery.ClusterAffiliateID{discovery.ClusterID(cluster), discovery.AffiliateID(affiliate)}
}
