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
	"encoding/json"
	"log/slog"
	"os"
)

type JSONFileStateProvider struct {
	Logger *slog.Logger
	Path   string
}

var _ StateProvider = &JSONFileStateProvider{}

func (p *JSONFileStateProvider) Load() []ClusterAffiliate {
	content, err := os.ReadFile(p.Path)
	if os.IsNotExist(err) {
		err := os.WriteFile(p.Path, []byte("[]"), 0o666)
		if err != nil {
			p.Logger.Error("could not create initial json file", "err", err, "path", p.Path)
		}

		return nil
	}
	if err != nil {
		if p.Logger != nil {
			p.Logger.Error("could not read file", "err", err, "path", p.Path)
		}

		return nil
	}

	var affiliates []ClusterAffiliate
	err = json.Unmarshal(content, &affiliates)
	if err != nil {
		if p.Logger != nil {
			p.Logger.Error("could not unmarshal json", "err", err, "path", p.Path)
		}

		return nil
	}

	return affiliates
}

func (p *JSONFileStateProvider) Save(affiliates []ClusterAffiliate) {
	data, err := json.Marshal(affiliates)
	if err != nil {
		if p.Logger != nil {
			p.Logger.Error("could not marshal json", "err", err)
		}

		return
	}
	file, err := os.Create(p.Path)
	if err != nil {
		if p.Logger != nil {
			p.Logger.Error("could not create file", "err", err, "path", p.Path)
		}

		return
	}

	_, err = file.Write(data)
	if err != nil {
		if p.Logger != nil {
			p.Logger.Error("could not write to file", "err", err, "path", p.Path)
		}

		return
	}

	err = file.Sync()
	if err != nil {
		if p.Logger != nil {
			p.Logger.Error("could not sync file", "err", err, "path", p.Path)
		}

		return
	}

	if p.Logger != nil {
		p.Logger.Debug("updated state", "path", p.Path)
	}
}
