"""Verify N1X recommendations use PCI identity rather than driver display names.

Simulated nvidia-smi output feeds the production memory probe, HardwareBudget and
catalog recommender. N1X IDs must still qualify after a name change; conflicting
IDs must not qualify through a matching name. Unavailable IDs retain the existing
name fallback without changing memory budgets or recipe eligibility.

Only the hardware responses are simulated. These are functional regression tests,
not physical-device validation or performance benchmarks.
"""

from types import SimpleNamespace

import pytest

from hermes_cli.local_runtime import hardware


@pytest.mark.parametrize("integrated", [False, True])
def test_unavailable_pci_id_preserves_memory_and_name_fallback(monkeypatch, integrated):
    calls = []
    monkeypatch.setattr(hardware, "_nvidia_smi_path", lambda: "nvidia-smi")
    monkeypatch.setattr(hardware, "_gpu_query_cache", None)
    monkeypatch.setattr(hardware, "_ram_bytes", lambda: (64 << 30, 22 << 30))
    monkeypatch.setattr(hardware, "_device_pool_view", lambda: (48 << 30, integrated))
    name = "NVIDIA RTX Spark N1X"

    def run(argv, **kwargs):
        calls.append(argv)
        assert argv[1] == "--query-gpu=memory.total,memory.free,name,pci.device_id,memory.used,utilization.gpu"
        return SimpleNamespace(returncode=0, stdout=f"32704, 31423, {name}, N/A, 2048, 7\n")

    monkeypatch.setattr(hardware.subprocess, "run", run)
    budget = hardware.probe_budget(planning=True)
    assert budget.gpu_name == name
    assert budget.gpu_pci_id is None
    assert budget.uma == integrated
    total = 48 << 30 if integrated else 32704 << 20
    usable = int(total * .8) if integrated else total - max(2 << 30, int(total * .09))
    assert budget.total_device_bytes == total
    assert budget.usable_vram_bytes == usable
    assert len(calls) == 1
