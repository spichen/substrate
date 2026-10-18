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

package ocispec

import (
	"crypto/sha256"
	"fmt"
	"github.com/agent-substrate/substrate/internal/proto/ateletpb"
	"path/filepath"
	"slices"

	"github.com/agent-substrate/substrate/internal/imagecache"
	"github.com/agent-substrate/substrate/internal/proto/ateompb"
)

// SubPathDir holds pinned per-mount sources, outside the sandbox's rootfs.
const SubPathDir = "subpaths"

type volumeBind struct {
	mount    *ateletpb.VolumeMount
	source   string
	readOnly bool
}

func volumeBinds(o Options) []volumeBind {
	volumes := make(map[string]*ateletpb.Volume, len(o.Volumes))
	for _, v := range o.Volumes {
		volumes[v.GetName()] = v
	}
	var out []volumeBind
	for _, m := range o.VolumeMounts {
		root, ro := "", m.GetReadOnly()
		switch volumes[m.GetName()].GetSource().(type) {
		case *ateletpb.Volume_DurableDir:
			root = o.DurableDirVolumeMountsDir
		case *ateletpb.Volume_External, *ateletpb.Volume_EmptyDir:
			root = o.VolumesDir
		case *ateletpb.Volume_SystemInfo:
			root, ro = o.SystemInfoVolumeRootsDir, true
		case *ateletpb.Volume_Image:
			root, ro = imagecache.ImageVolumeMountPath(o.BundlePath, ""), true
		default:
			continue
		}
		out = append(out, volumeBind{m, filepath.Join(root, m.GetName()), ro})
	}
	return out
}

func subPathTarget(dir, destination string) string {
	return filepath.Join(dir, fmt.Sprintf("%x", sha256.Sum256([]byte(destination))))
}

type runtimeMount interface {
	GetVolumeName() string
	GetMountPath() string
	GetReadOnly() bool
	GetSubPath() string
}

// RuntimeMountOptions reconstructs volume sources from the worker request.
func RuntimeMountOptions(d *ateompb.ActorDirs, c *ateompb.Container) Options {
	o := Options{BundlePath: filepath.Join(d.GetOciBundleDir(), c.GetName()), DurableDirVolumeMountsDir: d.GetDurableDirVolumeMountsDir(), VolumesDir: d.GetVolumesDir(), SystemInfoVolumeRootsDir: d.GetSystemInfoVolumeRootsDir()}
	add := func(m runtimeMount, v *ateletpb.Volume) {
		v.Name = m.GetVolumeName()
		o.Volumes = append(o.Volumes, v)
		o.VolumeMounts = append(o.VolumeMounts, &ateletpb.VolumeMount{Name: m.GetVolumeName(), MountPath: m.GetMountPath(), ReadOnly: m.GetReadOnly(), SubPath: m.GetSubPath()})
	}
	for _, m := range c.GetDurableDirVolumeMounts() {
		add(m, &ateletpb.Volume{Source: &ateletpb.Volume_DurableDir{DurableDir: &ateletpb.DurableDirVolume{}}})
	}
	for _, m := range c.GetCsiVolumeMounts() {
		add(m, &ateletpb.Volume{Source: &ateletpb.Volume_External{External: &ateletpb.ExternalVolumeSource{}}})
	}
	for _, m := range c.GetSystemInfoVolumeMounts() {
		add(m, &ateletpb.Volume{Source: &ateletpb.Volume_SystemInfo{SystemInfo: &ateletpb.SystemInfoVolume{}}})
	}
	for _, m := range c.GetImageVolumeMounts() {
		add(m, &ateletpb.Volume{Source: &ateletpb.Volume_Image{Image: &ateletpb.ImageVolumeSource{}}})
	}
	return o
}

// WholeVolumeNames returns only volumes whose root is explicitly requested.
func WholeVolumeNames[M runtimeMount](containers []*ateompb.Container, mounts func(*ateompb.Container) []M) []string {
	var names []string
	for _, c := range containers {
		for _, m := range mounts(c) {
			if m.GetSubPath() == "" {
				names = append(names, m.GetVolumeName())
			}
		}
	}
	slices.Sort(names)
	return slices.Compact(names)
}
