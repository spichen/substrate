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

package functionaltest

import (
	"context"
	"slices"
	"testing"

	"go.ate.dev/substrate/internal/proto/ateletpb"
	"go.ate.dev/substrate/internal/resources"
	"go.ate.dev/substrate/pkg/proto/ateapipb"
	"google.golang.org/grpc/codes"
)

func resetAteletCalls(tc *testContext) {
	tc.fakeAtelet.Lock.Lock()
	defer tc.fakeAtelet.Lock.Unlock()
	tc.fakeAtelet.RunCalled, tc.fakeAtelet.RunRequest = false, nil
	tc.fakeAtelet.RestoreCalled, tc.fakeAtelet.RestoreRequest = false, nil
}

func ateletCalls(tc *testContext) (run, restore bool) {
	tc.fakeAtelet.Lock.Lock()
	defer tc.fakeAtelet.Lock.Unlock()
	return tc.fakeAtelet.RunCalled, tc.fakeAtelet.RestoreCalled
}

func getActor(t *testing.T, tc *testContext, name string) *ateapipb.Actor {
	t.Helper()
	actor, err := tc.client.GetActor(context.Background(), &ateapipb.GetActorRequest{
		Actor: &ateapipb.ObjectRef{Atespace: testAtespace, Name: name},
	})
	if err != nil {
		t.Fatalf("GetActor(%s) failed: %v", name, err)
	}
	return actor
}

func TestSharedVolumeRef_ColdBootsThenRestoresOwnSnapshot(t *testing.T) {
	ns := namespaceForTest("ns-extvol-coldboot")
	tc, _ := setupSharedVolumeTest(t, ns)
	defer tc.cleanup()

	createSharedVolume(t, tc, "shared", ateapipb.DeleteTrigger_DELETE_TRIGGER_MANUAL)
	tmpl := createTemplateWithVolumes(t, tc, ns,
		[]*ateapipb.Volume{{Name: "work", External: &ateapipb.ExternalVolumeSource{SharedVolume: &ateapipb.SharedVolumeRef{Name: "shared"}}}},
		[]*ateapipb.VolumeMount{{Name: "work", MountPath: "/mnt/work"}})
	if tmpl.GetStatus().GetGoldenSnapshotStatus().GetGoldenTag() == nil {
		t.Fatal("test template has no golden tag; the case needs one to prove it is bypassed")
	}
	workerName := createWorkerPod(t, tc, ns, "worker-1", "node1", "pool1")
	ctx := context.Background()
	ref := &ateapipb.ObjectRef{Atespace: testAtespace, Name: "borrower"}

	createBorrowingActor(t, tc, "borrower", nil)
	if snaps := getActor(t, tc, "borrower").GetStatus().GetSnapshots(); len(snaps) != 0 {
		t.Fatalf("new actor carries snapshots %v, want none: it must not seed from the golden", snaps)
	}

	waitForWorkerAvailable(t, tc, workerName)
	resetAteletCalls(tc)
	if _, err := tc.client.ResumeActor(ctx, &ateapipb.ResumeActorRequest{Actor: ref}); err != nil {
		t.Fatalf("first ResumeActor failed: %v", err)
	}
	if run, restore := ateletCalls(tc); !run || restore {
		t.Fatalf("first resume: Run=%v Restore=%v, want a cold boot through Run", run, restore)
	}

	if _, err := tc.client.SuspendActor(ctx, &ateapipb.SuspendActorRequest{Actor: ref}); err != nil {
		t.Fatalf("SuspendActor failed: %v", err)
	}
	own := durableSnapshotURI(getActor(t, tc, "borrower").GetStatus())
	if own == "" || own == goldenSnapshotURI(t) {
		t.Fatalf("snapshot after suspend = %q, want the actor's own", own)
	}

	waitForWorkerAvailable(t, tc, workerName)
	resetAteletCalls(tc)
	if _, err := tc.client.ResumeActor(ctx, &ateapipb.ResumeActorRequest{Actor: ref}); err != nil {
		t.Fatalf("second ResumeActor failed: %v", err)
	}
	if run, restore := ateletCalls(tc); run || !restore {
		t.Fatalf("second resume: Run=%v Restore=%v, want a restore", run, restore)
	}
	if got := tc.fakeAtelet.lastRestoreRequest().GetSnapshot().GetObject().GetSnapshotUri(); got != own {
		t.Errorf("second resume restored %q, want the actor's own snapshot %q", got, own)
	}
}

func TestSharedVolumeRef_RejectsSourceTag(t *testing.T) {
	ns := namespaceForTest("ns-extvol-sourcetag")
	tc, _ := setupSharedVolumeTest(t, ns)
	defer tc.cleanup()

	createSharedVolume(t, tc, "shared", ateapipb.DeleteTrigger_DELETE_TRIGGER_MANUAL)
	tmpl := createTemplateWithVolumes(t, tc, ns,
		[]*ateapipb.Volume{{Name: "work", External: &ateapipb.ExternalVolumeSource{SharedVolume: &ateapipb.SharedVolumeRef{Name: "shared"}}}},
		[]*ateapipb.VolumeMount{{Name: "work", MountPath: "/mnt/work"}})

	_, err := tc.client.CreateActor(context.Background(), &ateapipb.CreateActorRequest{Actor: &ateapipb.Actor{
		Metadata:      &ateapipb.ResourceMetadata{Atespace: testAtespace, Name: "clone"},
		ActorTemplate: &ateapipb.ObjectRef{Atespace: testAtespace, Name: "tmpl1"},
		SourceTag:     tmpl.GetStatus().GetGoldenSnapshotStatus().GetGoldenTag(),
	}})
	assertGrpcError(t, err, codes.FailedPrecondition, "Tag cloning does not support ActorTemplates that reference a SharedVolume")
}

func TestExternalVolumeTemplate_FirstResumeRestoresGolden(t *testing.T) {
	ns := namespaceForTest("ns-extvol-owned-golden")
	tc, _ := setupSharedVolumeTest(t, ns)
	defer tc.cleanup()

	createTemplateWithVolumes(t, tc, ns,
		[]*ateapipb.Volume{{Name: "work", External: &ateapipb.ExternalVolumeSource{Template: &ateapipb.ExternalVolumeTemplate{StorageClassName: "standard", Capacity: "1Gi"}}}},
		[]*ateapipb.VolumeMount{{Name: "work", MountPath: "/mnt/work"}})
	workerName := createWorkerPod(t, tc, ns, "worker-1", "node1", "pool1")
	ctx := context.Background()
	ref := &ateapipb.ObjectRef{Atespace: testAtespace, Name: "owner"}

	if _, err := tc.client.CreateActor(ctx, &ateapipb.CreateActorRequest{Actor: &ateapipb.Actor{
		Metadata:      &ateapipb.ResourceMetadata{Atespace: testAtespace, Name: "owner"},
		ActorTemplate: &ateapipb.ObjectRef{Atespace: testAtespace, Name: "tmpl1"},
	}}); err != nil {
		t.Fatalf("CreateActor failed: %v", err)
	}
	if got := durableSnapshotURI(getActor(t, tc, "owner").GetStatus()); got != goldenSnapshotURI(t) {
		t.Fatalf("new actor snapshot = %q, want the golden %q", got, goldenSnapshotURI(t))
	}

	waitForWorkerAvailable(t, tc, workerName)
	resetAteletCalls(tc)
	if _, err := tc.client.ResumeActor(ctx, &ateapipb.ResumeActorRequest{Actor: ref}); err != nil {
		t.Fatalf("ResumeActor failed: %v", err)
	}
	if run, restore := ateletCalls(tc); run || !restore {
		t.Fatalf("first resume: Run=%v Restore=%v, want a restore from the golden", run, restore)
	}
	if got := tc.fakeAtelet.lastRestoreRequest().GetSnapshot().GetObject().GetSnapshotUri(); got != goldenSnapshotURI(t) {
		t.Errorf("first resume restored %q, want the golden %q", got, goldenSnapshotURI(t))
	}
}

func goldenOverSharedVolumeContainers(mounts []*ateapipb.VolumeMount) []*ateapipb.Container {
	return []*ateapipb.Container{{
		Name:         "main",
		Image:        "main@sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		Command:      []string{"/main"},
		VolumeMounts: mounts,
	}}
}

func TestSharedVolumeRef_GoldenOverSharedVolumes(t *testing.T) {
	ns := namespaceForTest("ns-extvol-golden-shared")
	tc, _ := setupSharedVolumeTest(t, ns)
	defer tc.cleanup()

	shared := createSharedVolume(t, tc, "shared", ateapipb.DeleteTrigger_DELETE_TRIGGER_MANUAL)
	tmpl := createTemplateWithGoldenConfig(t, tc, ns,
		goldenOverSharedVolumeContainers([]*ateapipb.VolumeMount{
			{Name: "work", MountPath: "/mnt/work"},
			{Name: "work", MountPath: "/mnt/skills", SubPath: "skills", ReadOnly: true},
		}),
		[]*ateapipb.Volume{{Name: "work", External: &ateapipb.ExternalVolumeSource{SharedVolume: &ateapipb.SharedVolumeRef{Name: "shared"}}}},
		&ateapipb.GoldenSnapshotConfig{
			Mode:                 ateapipb.GoldenSnapshotMode_GOLDEN_SNAPSHOT_MODE_ENABLED,
			MountSharedVolumes:   true,
			SharedVolumeSubPaths: map[string]string{"work": ".golden"},
		})
	workerName := createWorkerPod(t, tc, ns, "worker-1", "node1", "pool1")
	ctx := context.Background()

	t.Run("golden actor records the shared volume", func(t *testing.T) {
		golden, err := tc.client.CreateActor(ctx, &ateapipb.CreateActorRequest{Actor: &ateapipb.Actor{
			Metadata:      &ateapipb.ResourceMetadata{Atespace: testAtespace, Name: tmpl.GetMetadata().GetUid()},
			ActorTemplate: &ateapipb.ObjectRef{Atespace: testAtespace, Name: "tmpl1"},
		}})
		if err != nil {
			t.Fatalf("CreateActor(golden) failed: %v", err)
		}
		vols := golden.GetStatus().GetExternalVolumes()
		if len(vols) != 1 || vols[0].GetStorageVolumeId() != shared.GetVolumeId() {
			t.Fatalf("golden actor external volumes = %v, want the SharedVolume %q", vols, shared.GetVolumeId())
		}
	})

	t.Run("actor restores the golden over its own directory", func(t *testing.T) {
		if _, err := tc.client.CreateActor(ctx, &ateapipb.CreateActorRequest{Actor: &ateapipb.Actor{
			Metadata:             &ateapipb.ResourceMetadata{Atespace: testAtespace, Name: "session-1"},
			ActorTemplate:        &ateapipb.ObjectRef{Atespace: testAtespace, Name: "tmpl1"},
			SharedVolumeSubPaths: map[string]string{"work": "tenant-a/session-1"},
		}}); err != nil {
			t.Fatalf("CreateActor failed: %v", err)
		}
		if got := durableSnapshotURI(getActor(t, tc, "session-1").GetStatus()); got != goldenSnapshotURI(t) {
			t.Fatalf("new actor snapshot = %q, want the golden %q", got, goldenSnapshotURI(t))
		}

		waitForWorkerAvailable(t, tc, workerName)
		resetAteletCalls(tc)
		if _, err := tc.client.ResumeActor(ctx, &ateapipb.ResumeActorRequest{Actor: &ateapipb.ObjectRef{Atespace: testAtespace, Name: "session-1"}}); err != nil {
			t.Fatalf("ResumeActor failed: %v", err)
		}
		if run, restore := ateletCalls(tc); run || !restore {
			t.Fatalf("first resume: Run=%v Restore=%v, want a restore from the golden", run, restore)
		}
		req := tc.fakeAtelet.lastRestoreRequest()
		if got := req.GetSnapshot().GetObject().GetSnapshotUri(); got != goldenSnapshotURI(t) {
			t.Errorf("first resume restored %q, want the golden %q", got, goldenSnapshotURI(t))
		}
		var subPaths []string
		for _, mount := range req.GetSpec().GetContainers()[0].GetVolumeMounts() {
			subPaths = append(subPaths, mount.GetSubPath())
		}
		if want := []string{"tenant-a/session-1", "tenant-a/session-1/skills"}; !slices.Equal(subPaths, want) {
			t.Errorf("restore mounts sub paths %v, want %v", subPaths, want)
		}
	})
}

func TestSharedVolumeRef_GoldenOverSharedVolumesValidation(t *testing.T) {
	ns := namespaceForTest("ns-extvol-golden-invalid")
	tc, _ := setupSharedVolumeTest(t, ns)
	defer tc.cleanup()
	ensureDefaultGvisorSandboxConfig(t, tc)

	named := &ateapipb.Volume{Name: "work", External: &ateapipb.ExternalVolumeSource{SharedVolume: &ateapipb.SharedVolumeRef{Name: "shared"}}}
	unnamed := &ateapipb.Volume{Name: "work", External: &ateapipb.ExternalVolumeSource{SharedVolume: &ateapipb.SharedVolumeRef{}}}
	writable := []*ateapipb.VolumeMount{{Name: "work", MountPath: "/mnt/work"}}
	for _, tt := range []struct {
		name   string
		volume *ateapipb.Volume
		golden *ateapipb.GoldenSnapshotConfig
		want   string
	}{
		{
			name:   "sub paths without mounting",
			volume: named,
			golden: &ateapipb.GoldenSnapshotConfig{Mode: ateapipb.GoldenSnapshotMode_GOLDEN_SNAPSHOT_MODE_ENABLED, SharedVolumeSubPaths: map[string]string{"work": ".golden"}},
			want:   `shared_volume_sub_paths requires mount_shared_volumes`,
		},
		{
			name:   "unnamed ref",
			volume: unnamed,
			golden: &ateapipb.GoldenSnapshotConfig{Mode: ateapipb.GoldenSnapshotMode_GOLDEN_SNAPSHOT_MODE_ENABLED, MountSharedVolumes: true, SharedVolumeSubPaths: map[string]string{"work": ".golden"}},
			want:   `every external.shared_volume to be named; volume "work" is not`,
		},
		{
			name:   "writable volume without a root",
			volume: named,
			golden: &ateapipb.GoldenSnapshotConfig{Mode: ateapipb.GoldenSnapshotMode_GOLDEN_SNAPSHOT_MODE_ENABLED, MountSharedVolumes: true},
			want:   `must root volume "work", which the template mounts writable`,
		},
		{
			name:   "root escapes the volume",
			volume: named,
			golden: &ateapipb.GoldenSnapshotConfig{Mode: ateapipb.GoldenSnapshotMode_GOLDEN_SNAPSHOT_MODE_ENABLED, MountSharedVolumes: true, SharedVolumeSubPaths: map[string]string{"work": "../golden"}},
			want:   `shared_volume_sub_paths\["work"\]`,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := tc.client.CreateActorTemplate(context.Background(), &ateapipb.CreateActorTemplateRequest{ActorTemplate: &ateapipb.ActorTemplate{
				Metadata:       &ateapipb.ResourceMetadata{Atespace: testAtespace, Name: "invalid"},
				SnapshotConfig: &ateapipb.SnapshotConfig{StorageLocation: testStorageLocation, GoldenSnapshotConfig: tt.golden},
				Containers:     goldenOverSharedVolumeContainers(writable),
				Volumes:        []*ateapipb.Volume{tt.volume},
				WorkerSelector: &ateapipb.Selector{MatchLabels: map[string]string{poolLabelKey: ns}},
				Resources:      resources.CPUMemory(1000, 512<<20),
			}})
			assertGrpcErrorRegex(t, err, codes.InvalidArgument, tt.want)
		})
	}
}

func TestActorVolumeMounts_RestoreVolumesOnlyAfterAChange(t *testing.T) {
	ns := namespaceForTest("ns-extvol-actor-mounts")
	tc, _ := setupSharedVolumeTest(t, ns)
	defer tc.cleanup()

	createSharedVolume(t, tc, "shared", ateapipb.DeleteTrigger_DELETE_TRIGGER_MANUAL)
	createTemplateWithVolumes(t, tc, ns,
		[]*ateapipb.Volume{{Name: "work", External: &ateapipb.ExternalVolumeSource{SharedVolume: &ateapipb.SharedVolumeRef{Name: "shared"}}}},
		[]*ateapipb.VolumeMount{{Name: "work", MountPath: "/mnt/work"}})
	workerName := createWorkerPod(t, tc, ns, "worker-1", "node1", "pool1")
	ctx := context.Background()
	ref := &ateapipb.ObjectRef{Atespace: testAtespace, Name: "mounted"}
	history := &ateapipb.ActorVolumeMount{Container: "main", VolumeMount: &ateapipb.VolumeMount{Name: "work", MountPath: "/mnt/work/history", SubPath: "history", ReadOnly: true}}

	if _, err := tc.client.CreateActor(ctx, &ateapipb.CreateActorRequest{Actor: &ateapipb.Actor{
		Metadata:      &ateapipb.ResourceMetadata{Atespace: testAtespace, Name: "mounted"},
		ActorTemplate: &ateapipb.ObjectRef{Atespace: testAtespace, Name: "tmpl1"},
		VolumeMounts:  []*ateapipb.ActorVolumeMount{history},
	}}); err != nil {
		t.Fatalf("CreateActor failed: %v", err)
	}
	if snaps := getActor(t, tc, "mounted").GetStatus().GetSnapshots(); len(snaps) != 0 {
		t.Fatalf("actor with its own mounts carries snapshots %v, want none: the golden was taken over other mounts", snaps)
	}

	waitForWorkerAvailable(t, tc, workerName)
	resetAteletCalls(tc)
	if _, err := tc.client.ResumeActor(ctx, &ateapipb.ResumeActorRequest{Actor: ref}); err != nil {
		t.Fatalf("ResumeActor failed: %v", err)
	}
	var paths []string
	for _, mount := range tc.fakeAtelet.lastRunRequest().GetSpec().GetContainers()[0].GetVolumeMounts() {
		paths = append(paths, mount.GetMountPath())
	}
	if want := []string{"/mnt/work", "/mnt/work/history"}; !slices.Equal(paths, want) {
		t.Fatalf("run mounts = %v, want %v", paths, want)
	}

	update := func() error {
		actor := getActor(t, tc, "mounted")
		actor.VolumeMounts = []*ateapipb.ActorVolumeMount{history, {Container: "main", VolumeMount: &ateapipb.VolumeMount{Name: "work", MountPath: "/mnt/work/skills", SubPath: "skills", ReadOnly: true}}}
		_, err := tc.client.UpdateActor(ctx, &ateapipb.UpdateActorRequest{Actor: actor})
		return err
	}
	assertGrpcErrorRegex(t, update(), codes.FailedPrecondition, "to change its volume mounts")

	if _, err := tc.client.SuspendActor(ctx, &ateapipb.SuspendActorRequest{
		Actor:   ref,
		Options: &ateapipb.SuspendOptions{Mode: ateapipb.DurabilityMode_DURABILITY_MODE_SYNCHRONOUS},
	}); err != nil {
		t.Fatalf("SuspendActor failed: %v", err)
	}
	if err := update(); err != nil {
		t.Fatalf("UpdateActor while suspended failed: %v", err)
	}

	waitForWorkerAvailable(t, tc, workerName)
	resetAteletCalls(tc)
	if _, err := tc.client.ResumeActor(ctx, &ateapipb.ResumeActorRequest{Actor: ref}); err != nil {
		t.Fatalf("ResumeActor after the change failed: %v", err)
	}
	run, restore := ateletCalls(tc)
	if run {
		t.Fatal("resume after a mount change cold-booted without the actor's volumes")
	}
	if !restore {
		t.Fatal("resume after a mount change did not restore the actor's volumes")
	}
	if got := tc.fakeAtelet.lastRestoreRequest().GetSnapshot().GetFidelity(); got != ateletpb.SnapshotFidelity_SNAPSHOT_FIDELITY_VOLUMES {
		t.Errorf("restore fidelity after a mount change = %v, want volumes only", got)
	}
}
