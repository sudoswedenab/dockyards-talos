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
	"slices"
	"testing"
	"time"
	"weak"

	"github.com/sudoswedenab/dockyards-talos/internal/sync"

	discoveryv1 "github.com/siderolabs/discovery-api/api/v1alpha1/server/pb"
	"google.golang.org/protobuf/types/known/durationpb"
)

func TestAffiliateUpdateBroadcastsForExistingAffiliate(t *testing.T) {
	ch := make(chan WatchResponse, 1)

	affiliateID := AffiliateID("some-affiliate")
	clusterID := ClusterID("some-cluster")

	server := ClusterDiscoveryServer{
		watchers: sync.NewMutexProtected([]weak.Pointer[chan WatchResponse]{
			weak.Make(&ch),
		}),
		ClusterAffiliates: sync.NewMutexProtected(map[ClusterAffiliateID]ClusterAffiliate{
			{ClusterID: clusterID, AffiliateID: affiliateID}: {
				ClusterID: clusterID,
				Affiliate: &discoveryv1.Affiliate{
					Id:        string(affiliateID),
					Endpoints: [][]byte{[]byte("old-endpoint")},
				},
				RemoveAfter: time.Now().Add(15 * time.Minute),
			},
		}),
	}

	_, err := server.AffiliateUpdate(t.Context(), &discoveryv1.AffiliateUpdateRequest{
		ClusterId:          string(clusterID),
		AffiliateId:        string(affiliateID),
		AffiliateEndpoints: [][]byte{[]byte("new-endpoint")},
		Ttl:                durationpb.New(10 * time.Minute),
	})
	if err != nil {
		t.Fatalf("affiliate update failed: %v", err)
	}

	select {
	case msg := <-ch:
		if msg.Deleted {
			t.Fatalf("expected non-deleted update")
		}

		if len(msg.ClusterAffiliates) != 1 {
			t.Fatalf("expected 1 cluster affiliate in update, got %d", len(msg.ClusterAffiliates))
		}

		aff := msg.ClusterAffiliates[0].Affiliate
		if aff == nil {
			t.Fatalf("expected affiliate in update message")
		}

		if !slices.ContainsFunc(aff.Endpoints, func(endpoint []byte) bool {
			return string(endpoint) == "new-endpoint"
		}) {
			t.Fatalf("expected updated endpoints to contain new-endpoint, got %q", aff.Endpoints)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for update broadcast")
	}
}
