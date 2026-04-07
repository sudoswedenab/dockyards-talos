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

type StateProvider interface {
	Save([]ClusterAffiliate)
	Load() []ClusterAffiliate
}

type SaveStateFunc func([]ClusterAffiliate)

type WatchStateProvider struct {
	StateProvider
	watch SaveStateFunc
}

func AddWatch(p StateProvider, watch SaveStateFunc) StateProvider {
	if watch == nil {
		return p
	}

	return &WatchStateProvider{
		StateProvider: p,
		watch:         watch,
	}
}

func (p *WatchStateProvider) Save(a []ClusterAffiliate) {
	p.StateProvider.Save(a)
	p.watch(a)
}
