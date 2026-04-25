package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// fakeSinkhole records every Engage / Release / RecoverFromCrash
// call. Used to test the controller without touching the OS.
type fakeSinkhole struct {
	engageCalls   atomic.Int32
	releaseCalls  atomic.Int32
	recoverCalls  atomic.Int32
	engageErr     error
	releaseErr    error
}

func (f *fakeSinkhole) Engage(ctx context.Context) error {
	f.engageCalls.Add(1)
	return f.engageErr
}
func (f *fakeSinkhole) Release(ctx context.Context) error {
	f.releaseCalls.Add(1)
	return f.releaseErr
}
func (f *fakeSinkhole) RecoverFromCrash(ctx context.Context) error {
	f.recoverCalls.Add(1)
	return nil
}

func TestSinkholeController_RunCallsRecoverFirst(t *testing.T) {
	ks := NewKillSwitchManager()
	fake := &fakeSinkhole{}
	c := NewSinkholeController(ks, fake)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go c.Run(ctx)
	time.Sleep(50 * time.Millisecond) // let RecoverFromCrash run

	if got := fake.recoverCalls.Load(); got != 1 {
		t.Fatalf("RecoverFromCrash should be called exactly once, got %d", got)
	}
	if got := fake.engageCalls.Load(); got != 0 {
		t.Fatalf("Engage should not be called yet, got %d", got)
	}
}

func TestSinkholeController_EngagesOnSinkholeTransition(t *testing.T) {
	ks := NewKillSwitchManager()
	fake := &fakeSinkhole{}
	c := NewSinkholeController(ks, fake)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)
	time.Sleep(50 * time.Millisecond)

	ks.ForceSinkhole("test")
	time.Sleep(100 * time.Millisecond)

	if got := fake.engageCalls.Load(); got != 1 {
		t.Fatalf("Engage should fire once on SINKHOLE transition, got %d", got)
	}
	if !c.IsEngaged() {
		t.Fatal("controller should report engaged after successful Engage")
	}
}

func TestSinkholeController_ReleasesOnExitFromSinkhole(t *testing.T) {
	ks := NewKillSwitchManager()
	fake := &fakeSinkhole{}
	c := NewSinkholeController(ks, fake)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)
	time.Sleep(50 * time.Millisecond)

	ks.ForceSinkhole("test")
	time.Sleep(100 * time.Millisecond)
	ks.Disarm() // SINKHOLE -> IDLE: triggers Release
	time.Sleep(100 * time.Millisecond)

	if got := fake.releaseCalls.Load(); got != 1 {
		t.Fatalf("Release should fire once on exit from SINKHOLE, got %d", got)
	}
	if c.IsEngaged() {
		t.Fatal("controller should NOT report engaged after Release")
	}
}

func TestSinkholeController_NoEngageWhenAlreadyEngaged(t *testing.T) {
	ks := NewKillSwitchManager()
	fake := &fakeSinkhole{}
	c := NewSinkholeController(ks, fake)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)
	time.Sleep(50 * time.Millisecond)

	ks.ForceSinkhole("first")
	time.Sleep(100 * time.Millisecond)
	ks.ForceSinkhole("second") // should be a no-op (already SINKHOLE)
	time.Sleep(100 * time.Millisecond)

	if got := fake.engageCalls.Load(); got != 1 {
		t.Fatalf("Engage should fire only once across two ForceSinkhole calls, got %d", got)
	}
}

func TestSinkholeController_FailedEngageLeavesEngagedFalse(t *testing.T) {
	ks := NewKillSwitchManager()
	fake := &fakeSinkhole{engageErr: errors.New("simulated firewall failure")}
	c := NewSinkholeController(ks, fake)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)
	time.Sleep(50 * time.Millisecond)

	ks.ForceSinkhole("test")
	time.Sleep(100 * time.Millisecond)

	if got := fake.engageCalls.Load(); got != 1 {
		t.Fatalf("Engage should be attempted once, got %d", got)
	}
	if c.IsEngaged() {
		t.Fatal("after failed Engage, IsEngaged must remain false")
	}
}

func TestSinkholeController_StopReleasesIfEngaged(t *testing.T) {
	ks := NewKillSwitchManager()
	fake := &fakeSinkhole{}
	c := NewSinkholeController(ks, fake)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)
	time.Sleep(50 * time.Millisecond)

	ks.ForceSinkhole("test")
	time.Sleep(100 * time.Millisecond)
	if !c.IsEngaged() {
		t.Fatal("setup: should be engaged")
	}

	c.Stop()
	time.Sleep(100 * time.Millisecond)

	if got := fake.releaseCalls.Load(); got != 1 {
		t.Fatalf("Stop should trigger one Release, got %d", got)
	}
}

func TestSnapshot_SaveLoadDelete_RoundTrip(t *testing.T) {
	// Override snapshot path to a tmp dir for the test.
	tmpDir := t.TempDir()
	tmpFile := filepath.Join(tmpDir, "sinkhole-snapshot.json")
	prev := snapshotPathOverride
	snapshotPathOverride = tmpFile
	defer func() { snapshotPathOverride = prev }()

	in := &SinkholeSnapshot{
		Version:            1,
		EngagedAt:          time.Now().Round(time.Second),
		Platform:           "test",
		Reason:             "round-trip test",
		FirewallRulesAdded: []string{"rule-a", "rule-b"},
	}
	if err := SaveSnapshot(in); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}

	out, err := LoadSnapshot()
	if err != nil {
		t.Fatalf("LoadSnapshot: %v", err)
	}
	if out.Reason != in.Reason {
		t.Fatalf("Reason: want %q got %q", in.Reason, out.Reason)
	}
	if len(out.FirewallRulesAdded) != 2 || out.FirewallRulesAdded[0] != "rule-a" {
		t.Fatalf("FirewallRulesAdded: want [rule-a rule-b], got %v", out.FirewallRulesAdded)
	}
	if !out.EngagedAt.Equal(in.EngagedAt) {
		t.Fatalf("EngagedAt: want %v got %v", in.EngagedAt, out.EngagedAt)
	}

	if err := DeleteSnapshot(); err != nil {
		t.Fatalf("DeleteSnapshot: %v", err)
	}
	if _, err := LoadSnapshot(); !os.IsNotExist(err) {
		t.Fatalf("after delete, LoadSnapshot should return IsNotExist, got %v", err)
	}
}

func TestSnapshot_DeleteIdempotent(t *testing.T) {
	tmpDir := t.TempDir()
	tmpFile := filepath.Join(tmpDir, "sinkhole-snapshot.json")
	prev := snapshotPathOverride
	snapshotPathOverride = tmpFile
	defer func() { snapshotPathOverride = prev }()

	// Delete when nothing is there should NOT error.
	if err := DeleteSnapshot(); err != nil {
		t.Fatalf("DeleteSnapshot on missing file: %v", err)
	}
}
