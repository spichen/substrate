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

package csi

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"go.ate.dev/substrate/internal/volume"
)

// All calls go through the CSI gRPC server, including capability discovery.
func TestPlugin_ReadWriteMany(t *testing.T) {
	for _, scenario := range []string{"supported", "attachment", "staging", "discovery failure", "undiscovered"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			var mu sync.Mutex
			published := map[string]bool{}
			creates, publishes, unpublishes := 0, 0, 0
			driver := &mockCSIDriver{}
			driver.controllerGetCapabilitiesFunc = func(context.Context, *csi.ControllerGetCapabilitiesRequest) (*csi.ControllerGetCapabilitiesResponse, error) {
				if scenario == "discovery failure" {
					return nil, fmt.Errorf("unavailable")
				}
				resp := &csi.ControllerGetCapabilitiesResponse{}
				if scenario == "attachment" {
					resp.Capabilities = []*csi.ControllerServiceCapability{{Type: &csi.ControllerServiceCapability_Rpc{Rpc: &csi.ControllerServiceCapability_RPC{Type: csi.ControllerServiceCapability_RPC_PUBLISH_UNPUBLISH_VOLUME}}}}
				}
				return resp, nil
			}
			driver.nodeGetCapabilitiesFunc = func(context.Context, *csi.NodeGetCapabilitiesRequest) (*csi.NodeGetCapabilitiesResponse, error) {
				if scenario == "discovery failure" {
					return nil, fmt.Errorf("unavailable")
				}
				resp := &csi.NodeGetCapabilitiesResponse{}
				if scenario == "staging" {
					resp.Capabilities = []*csi.NodeServiceCapability{{Type: &csi.NodeServiceCapability_Rpc{Rpc: &csi.NodeServiceCapability_RPC{Type: csi.NodeServiceCapability_RPC_STAGE_UNSTAGE_VOLUME}}}}
				}
				return resp, nil
			}
			checkCapability := func(capability *csi.VolumeCapability) {
				if capability.GetAccessMode().GetMode() != csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER {
					t.Errorf("wrong capability: %v", capability)
				}
			}
			driver.createVolumeFunc = func(_ context.Context, req *csi.CreateVolumeRequest) (*csi.CreateVolumeResponse, error) {
				mu.Lock()
				defer mu.Unlock()
				creates++
				for _, capability := range req.GetVolumeCapabilities() {
					checkCapability(capability)
				}
				return &csi.CreateVolumeResponse{Volume: &csi.Volume{VolumeId: "shared"}}, nil
			}
			driver.controllerPublishVolumeFunc = func(context.Context, *csi.ControllerPublishVolumeRequest) (*csi.ControllerPublishVolumeResponse, error) {
				t.Error("RWX must not attach")
				return nil, fmt.Errorf("unexpected attach")
			}
			driver.nodeStageVolumeFunc = func(context.Context, *csi.NodeStageVolumeRequest) (*csi.NodeStageVolumeResponse, error) {
				t.Error("RWX must not stage")
				return nil, fmt.Errorf("unexpected stage")
			}
			driver.nodeUnstageVolumeFunc = func(context.Context, *csi.NodeUnstageVolumeRequest) (*csi.NodeUnstageVolumeResponse, error) {
				t.Error("RWX must not unstage")
				return nil, fmt.Errorf("unexpected unstage")
			}
			driver.nodePublishVolumeFunc = func(_ context.Context, req *csi.NodePublishVolumeRequest) (*csi.NodePublishVolumeResponse, error) {
				mu.Lock()
				defer mu.Unlock()
				publishes++
				checkCapability(req.GetVolumeCapability())
				if req.GetStagingTargetPath() != "" {
					t.Error("unexpected staging path")
				}
				published[req.GetTargetPath()] = true
				return &csi.NodePublishVolumeResponse{}, nil
			}
			driver.nodeUnpublishVolumeFunc = func(_ context.Context, req *csi.NodeUnpublishVolumeRequest) (*csi.NodeUnpublishVolumeResponse, error) {
				mu.Lock()
				defer mu.Unlock()
				unpublishes++
				delete(published, req.GetTargetPath())
				return &csi.NodeUnpublishVolumeResponse{}, nil
			}
			endpoint, cleanup := startMockCSIDriver(t, driver)
			defer cleanup()
			client, err := NewCSIClient(endpoint, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			plugin := NewPlugin(client)
			if scenario != "undiscovered" {
				plugin.InitControllerCapabilities(ctx)
				plugin.InitNodeCapabilities(ctx)
			}
			_, err = plugin.CreateVolume(ctx, volume.CreateVolumeRequest{Name: "shared", Capacity: "1Gi", ReadWriteMany: true})
			controllerSafe := scenario == "supported" || scenario == "staging"
			if (err == nil) != controllerSafe {
				t.Fatalf("CreateVolume error = %v, safe = %v", err, controllerSafe)
			}
			_, err = plugin.AttachVolume(ctx, volume.AttachVolumeRequest{VolumeID: "shared", Node: "node1", ReadWriteMany: true})
			if (err == nil) != controllerSafe {
				t.Fatalf("AttachVolume error = %v, safe = %v", err, controllerSafe)
			}
			nodeSafe := scenario == "supported" || scenario == "attachment"
			for _, target := range []string{"/actors/a/shared", "/actors/b/shared"} {
				err = plugin.MountVolume(ctx, volume.MountVolumeRequest{VolumeID: "shared", TargetPath: target, ReadWriteMany: true})
				if (err == nil) != nodeSafe {
					t.Fatalf("MountVolume error = %v, safe = %v", err, nodeSafe)
				}
			}
			if !nodeSafe {
				if publishes != 0 {
					t.Fatalf("rejected driver received %d publishes", publishes)
				}
			} else {
				// Recreate the plugin as after an atelet restart. Cleanup must not rely
				// on an in-memory reference count or refreshed driver capabilities.
				plugin = NewPlugin(client)
				for range 2 {
					if err := plugin.UnmountVolume(ctx, volume.UnmountVolumeRequest{VolumeID: "shared", TargetPath: "/actors/a/shared", ReadWriteMany: true}); err != nil {
						t.Fatal(err)
					}
				}
				mu.Lock()
				if !published["/actors/b/shared"] || published["/actors/a/shared"] {
					t.Errorf("cleanup affected another actor: %v", published)
				}
				mu.Unlock()
			}
			if !controllerSafe && creates != 0 {
				t.Errorf("rejected driver received %d creates", creates)
			}
		})
	}
}
