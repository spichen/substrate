//go:build linux

// Copyright 2026 Google LLC
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

package main

import (
	"context"
	"path/filepath"

	"github.com/agent-substrate/substrate/cmd/ateom-microvm/internal/kata"
	"github.com/agent-substrate/substrate/internal/ocispec"
	"github.com/agent-substrate/substrate/internal/proto/ateompb"
)

// hasCsiVolumes reports whether any container mounts a CSI volume.
func hasCsiVolumes(containers []*ateompb.Container) bool {
	for _, c := range containers {
		if len(c.GetCsiVolumeMounts()) > 0 {
			return true
		}
	}
	return false
}

// stageCsiVolumes exposes only explicitly requested whole-volume roots.
func (s *AteomService) stageCsiVolumes(ctx context.Context, actorUID, src string, names []string) error {
	for _, name := range names {
		if err := kata.BindIntoShare(ctx, filepath.Join(src, name), actorUID, filepath.Join(ocispec.ShareCSI, name)); err != nil {
			return err
		}
	}
	return nil
}
