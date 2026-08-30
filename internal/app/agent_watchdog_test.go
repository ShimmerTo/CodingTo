package app

import (
	"testing"
	"time"
)

func TestParallelToolWatchdogsRemainIndependent(t *testing.T) {
	service := &AgentService{toolExecutionTimeout: time.Hour}
	service.armToolWatchdogLocked(7, "bash", "call-a")
	service.armToolWatchdogLocked(7, "bash", "call-b")
	t.Cleanup(func() { service.disarmAllToolWatchdogsLocked() })

	if len(service.toolWatchdogs) != 2 {
		t.Fatalf("parallel calls should have two watchdogs, got %d", len(service.toolWatchdogs))
	}
	service.disarmToolWatchdogLocked("bash", "call-a")
	if service.toolWatchdogs["call-a"] != nil {
		t.Fatal("completed call still has a watchdog")
	}
	if service.toolWatchdogs["call-b"] == nil {
		t.Fatal("completing one parallel call cancelled the other call's watchdog")
	}
}

func TestUnmonitoredParallelToolDoesNotCancelBashWatchdog(t *testing.T) {
	service := &AgentService{toolExecutionTimeout: time.Hour}
	service.armToolWatchdogLocked(7, "bash", "call-bash")
	t.Cleanup(func() { service.disarmAllToolWatchdogsLocked() })

	service.armToolWatchdogLocked(7, "read", "call-read")
	if service.toolWatchdogs["call-bash"] == nil {
		t.Fatal("starting an unmonitored parallel tool cancelled the bash watchdog")
	}
	if len(service.toolWatchdogs) != 1 {
		t.Fatalf("unexpected watchdog count: %d", len(service.toolWatchdogs))
	}
}

func TestDuplicateToolStartReplacesOnlySameCall(t *testing.T) {
	service := &AgentService{toolExecutionTimeout: time.Hour}
	service.armToolWatchdogLocked(7, "bash", "call-a")
	first := service.toolWatchdogs["call-a"]
	service.armToolWatchdogLocked(7, "bash", "call-b")
	service.armToolWatchdogLocked(7, "BASH", "call-a")
	t.Cleanup(func() { service.disarmAllToolWatchdogsLocked() })

	if service.toolWatchdogs["call-a"] == first {
		t.Fatal("duplicate start did not replace the matching call watchdog")
	}
	if service.toolWatchdogs["call-b"] == nil || len(service.toolWatchdogs) != 2 {
		t.Fatal("duplicate start disturbed a different parallel call")
	}
}
