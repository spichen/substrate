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

package controlapi

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"

	"go.ate.dev/substrate/internal/apierror"
	"go.ate.dev/substrate/internal/volumepath"
	"go.ate.dev/substrate/pkg/proto/ateapipb"
	"google.golang.org/protobuf/proto"
)

// volumeMountsDigest identifies an actor's volume_mounts, so a snapshot can
// record the mounts it was taken over. It is "" for an actor without any.
func volumeMountsDigest(actor *ateapipb.Actor) string {
	mounts := actor.GetVolumeMounts()
	if len(mounts) == 0 {
		return ""
	}
	h := sha256.New()
	marshal := proto.MarshalOptions{Deterministic: true}
	for _, mount := range mounts {
		b, err := marshal.Marshal(mount)
		if err != nil {
			// Marshaling a validated message does not fail; a digest that
			// matches no snapshot only costs a cold boot.
			return "unmarshalable"
		}
		var size [8]byte
		binary.BigEndian.PutUint64(size[:], uint64(len(b)))
		h.Write(size[:])
		h.Write(b)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// mountsChangedSince reports whether the actor's volume_mounts differ from
// the ones snap was taken over. Restoring such a snapshot's memory would hand
// runsc a different mount table than the checkpoint's.
func mountsChangedSince(snap *ateapipb.Snapshot, actor *ateapipb.Actor) bool {
	return snap.GetVolumeMountsDigest() != volumeMountsDigest(actor)
}

// validateActorVolumeMounts checks the actor's own mounts against its
// template: each names a container and an external.shared_volume of the
// template, takes a mount path no other mount of that container uses, and
// stays a valid sub_path once joined with the actor's root for the volume.
func validateActorVolumeMounts(actor *ateapipb.Actor, template *ateapipb.ActorTemplate) error {
	shared := make(map[string]bool)
	for _, vol := range template.GetVolumes() {
		if vol.GetExternal().GetSharedVolume() != nil {
			shared[vol.GetName()] = true
		}
	}
	used := make(map[string]map[string]bool)
	for _, ctr := range template.GetContainers() {
		paths := make(map[string]bool)
		for _, mount := range ctr.GetVolumeMounts() {
			paths[mount.GetMountPath()] = true
		}
		used[ctr.GetName()] = paths
	}
	for i, actorMount := range actor.GetVolumeMounts() {
		paths, ok := used[actorMount.GetContainer()]
		if !ok {
			return apierror.InvalidArgument("volume_mounts[%d].container %q is not a container of the template", i, actorMount.GetContainer())
		}
		mount := actorMount.GetVolumeMount()
		if !shared[mount.GetName()] {
			return apierror.InvalidArgument("volume_mounts[%d].volume_mount.name %q does not name an external.shared_volume in the template", i, mount.GetName())
		}
		if paths[mount.GetMountPath()] {
			return apierror.InvalidArgument("volume_mounts[%d].volume_mount.mount_path %q is already mounted in container %q", i, mount.GetMountPath(), actorMount.GetContainer())
		}
		paths[mount.GetMountPath()] = true
		joined := mount.GetSubPath()
		if root := actor.GetSharedVolumeSubPaths()[mount.GetName()]; root != "" {
			joined = joinSubPath(root, joined)
		}
		if joined != "" {
			if err := volumepath.ValidateProjected(joined); err != nil {
				return apierror.InvalidArgument("volume_mounts[%d].volume_mount.sub_path joined with the actor's root: %v", i, err)
			}
		}
	}
	return nil
}
